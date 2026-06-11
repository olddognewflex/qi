// Command qid is the long-running local orchestrator for Qi. It hosts the
// tool registry and exposes a JSON-RPC 2.0 API over a unix-domain socket,
// callable by `qi` and (later) by qi-mcp on behalf of AI clients.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"qi/internal/approval"
	"qi/internal/calendar"
	"qi/internal/config"
	"qi/internal/daemon"
	"qi/internal/domain"
	"qi/internal/index"
	"qi/internal/mcp"
	"qi/internal/policy"
	"qi/internal/service"
	"qi/internal/skills"
	"qi/internal/tools"
	"qi/internal/tools/builtin"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "qid:", err)
		os.Exit(1)
	}
}

func run() error {
	var socketFlag string
	flag.StringVar(&socketFlag, "socket", "", "override socket path (defaults to XDG location)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	registry := tools.NewRegistry()
	if err := builtin.RegisterCapture(registry, cfg.InboxPath); err != nil {
		return fmt.Errorf("register capture: %w", err)
	}

	tasksSvc := service.TaskService{
		TaskFilePath: cfg.TaskFilePath,
		TasksDir:     filepath.Dir(cfg.TaskFilePath),
	}
	agendaSvc := service.AgendaService{Providers: buildAgendaProviders(cfg, log)}

	if err := builtin.RegisterTaskAdd(registry, tasksSvc); err != nil {
		return fmt.Errorf("register task.add: %w", err)
	}
	if err := builtin.RegisterTaskList(registry, tasksSvc); err != nil {
		return fmt.Errorf("register task.list: %w", err)
	}
	if err := builtin.RegisterNoteSearch(registry, indexSearcher{}); err != nil {
		return fmt.Errorf("register note.search: %w", err)
	}
	if err := builtin.RegisterAgendaToday(registry, agendaSvc); err != nil {
		return fmt.Errorf("register agenda.today: %w", err)
	}

	if err := skills.RegisterDailyReview(registry, tasksSvc, agendaSvc, cfg.InboxPath); err != nil {
		return fmt.Errorf("register daily-review: %w", err)
	}

	socketPath := socketFlag
	if socketPath == "" {
		socketPath, err = daemon.SocketPath()
		if err != nil {
			return err
		}
	}

	ln, err := daemon.Listen(socketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mcpMgr := mcp.NewManager(registry, log)
	defer func() { _ = mcpMgr.Close() }()
	for _, s := range cfg.MCPServers {
		spec := mcp.ServerSpec{ID: s.ID, Command: s.Command, Args: s.Args, Env: s.Env}
		if err := mcpMgr.Connect(ctx, spec); err != nil {
			log.Warn("mcp connect failed", "server", s.ID, "err", err)
			continue
		}
	}

	auditPath := filepath.Join(filepath.Dir(socketPath), "audit.log")
	audit, err := approval.OpenAudit(auditPath)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	defer func() { _ = audit.Close() }()
	queue := approval.NewQueue(audit)

	// Rebuild pending approvals from the audit log so a qid restart does not
	// silently drop calls that were awaiting human approval.
	if past, rerr := approval.ReadAuditLog(auditPath); rerr != nil {
		log.Warn("audit replay failed", "err", rerr)
	} else if restored := queue.Restore(past); restored > 0 {
		log.Info("restored pending approvals from audit log", "count", restored)
	}

	decider := policy.DefaultDecider{}

	log.Info("qid listening", "socket", socketPath, "audit", auditPath, "tools", registry.Len())

	server := daemon.NewServer(registry, decider, queue, log)
	if err := server.Serve(ctx, ln); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	log.Info("qid stopped")
	return nil
}

// indexSearcher adapts the SQLite note index to builtin.NoteSearcher. It opens
// the index per call (and closes it) so qid holds no long-lived DB handle and
// always reads the current derived state — the index may be rebuilt out of band
// by `qi index rebuild`.
type indexSearcher struct{}

func (indexSearcher) Search(query string) ([]domain.SearchResult, error) {
	idx, err := index.Open()
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	defer idx.Close()
	return idx.Search(query)
}

// buildAgendaProviders mirrors what `qi agenda` builds at CLI time but lives
// here so qid's skill layer has the same calendar sources. Only the local
// daily-notes provider is wired by default; future stages can add ICS,
// CalDAV, and Google providers here based on cfg.
func buildAgendaProviders(cfg config.Config, log *slog.Logger) []calendar.Provider {
	providers := []calendar.Provider{calendar.LocalProvider{PathFor: cfg.DailyNotePath}}
	_ = log
	return providers
}
