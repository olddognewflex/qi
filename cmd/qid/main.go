// Command qid is the long-running local orchestrator for Qi. It hosts the
// tool registry and exposes a JSON-RPC 2.0 API over a unix-domain socket,
// callable by `qi` and (later) by qi-mcp on behalf of AI clients.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"qi/internal/approval"
	"qi/internal/calendar"
	"qi/internal/config"
	"qi/internal/daemon"
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

	// TasksDir must be set so task.add routes by project; without it every task
	// would land in the inbox file regardless of its project tag.
	tasksSvc := service.TaskService{
		TaskFilePath: cfg.TaskFilePath,
		TasksDir:     filepath.Dir(cfg.TaskFilePath),
	}
	if err := builtin.RegisterTaskAdd(registry, tasksSvc, func(name string) bool {
		_, ok := cfg.ClientByName(name)
		return ok
	}); err != nil {
		return fmt.Errorf("register task.add: %w", err)
	}
	agendaSvc := service.AgendaService{Providers: buildAgendaProviders(cfg, log)}
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

	// Remote (HTTP) callers may run only the allowlisted tools directly; every
	// other mutation still routes through the approval queue via DefaultDecider.
	decider := policy.NewRemoteDecider(builtin.CaptureToolName, builtin.TaskAddToolName)

	log.Info("qid listening", "socket", socketPath, "audit", auditPath, "tools", registry.Len())

	server := daemon.NewServer(registry, decider, queue, log)

	// Optional HTTP endpoint for off-machine task creation (e.g. an iPhone
	// Shortcut over Tailscale). Only starts when explicitly enabled AND a token
	// is set, so a default daemon exposes no network surface.
	if cfg.Remote.Enabled && cfg.Remote.Token != "" {
		httpSrv := &http.Server{
			Addr:              cfg.Remote.Addr,
			Handler:           server.HTTPHandler(cfg.Remote.Token),
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			<-ctx.Done()
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutCtx)
		}()
		go func() {
			log.Info("qid http listening", "addr", cfg.Remote.Addr)
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("http server", "err", err)
			}
		}()
	} else if cfg.Remote.Enabled {
		log.Warn("remote enabled but no token set; HTTP endpoint not started")
	}

	if err := server.Serve(ctx, ln); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	log.Info("qid stopped")
	return nil
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
