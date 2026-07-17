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
	"strings"
	"syscall"
	"time"

	"qi/internal/approval"
	"qi/internal/calendar"
	"qi/internal/config"
	"qi/internal/daemon"
	"qi/internal/domain"
	"qi/internal/index"
	"qi/internal/mcp"
	"qi/internal/notify"
	"qi/internal/policy"
	"qi/internal/service"
	"qi/internal/skills"
	"qi/internal/sync"
	"qi/internal/tools"
	"qi/internal/tools/builtin"
	"qi/internal/watcher"
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
	agendaSvc := buildAgendaService(cfg, log)

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

	if err := skills.RegisterProcessInbox(registry, cfg.InboxPath); err != nil {
		return fmt.Errorf("register process-inbox: %w", err)
	}
	notesSvc := service.NoteService{NotesDir: cfg.NotesPath}
	inboxArchive := filepath.Join(cfg.InboxPath, "archive")
	if err := skills.RegisterProcessInboxApply(registry, cfg.InboxPath, inboxArchive, tasksSvc, notesSvc); err != nil {
		return fmt.Errorf("register process-inbox-apply: %w", err)
	}

	if err := skills.RegisterWeeklyReview(registry, tasksSvc, cfg.InboxPath, inboxArchive, cfg.DailyNotePath); err != nil {
		return fmt.Errorf("register weekly-review: %w", err)
	}
	if err := skills.RegisterWeeklyReviewApply(registry, notesSvc); err != nil {
		return fmt.Errorf("register weekly-review-apply: %w", err)
	}

	if err := skills.RegisterQuickTask(registry, tasksSvc); err != nil {
		return fmt.Errorf("register quick-task: %w", err)
	}
	if err := skills.RegisterSessionLog(registry, cfg.DailyNotePath); err != nil {
		return fmt.Errorf("register session-log: %w", err)
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

	// Opt-in fsnotify-driven auto-reconcile: when [sync] watch = true, watch the
	// vault task dirs and run the existing sync reconcile (debounced) on change.
	if cfg.Sync.Watch {
		if dirs := watcher.DirsFor(cfg); len(dirs) > 0 {
			debounce := time.Duration(cfg.Sync.DebounceMS) * time.Millisecond
			w, werr := watcher.New(watcher.Options{
				Dirs:     dirs,
				Debounce: debounce,
				OnChange: makeReconciler(cfg, log),
				Log:      log,
			})
			if werr != nil {
				return fmt.Errorf("sync watcher: %w", werr)
			}
			log.Info("sync watcher enabled", "dirs", dirs, "debounce_ms", cfg.Sync.DebounceMS)
			go func() {
				if err := w.Run(ctx); err != nil {
					log.Warn("sync watcher stopped", "err", err)
				}
			}()
		}
	}

	// Opt-in morning due-today notifier: when [notify] due_today = true, schedule
	// a once-a-day macOS notification (at [notify] at, default 08:00) listing
	// tasks due/scheduled today. Read-only, no policy gate. Off by default.
	if cfg.Notify.DueToday {
		notifier := notify.NewNotifier(log)
		s, nerr := notify.New(notify.Options{
			At:     cfg.Notify.At,
			OnFire: makeDueTodayNotifier(tasksSvc, notifier, log),
			Log:    log,
		})
		if nerr != nil {
			return fmt.Errorf("due-today notifier: %w", nerr)
		}
		at := cfg.Notify.At
		if at == "" {
			at = notify.DefaultAt
		}
		log.Info("due-today notifier enabled", "at", at)
		go func() {
			if err := s.Run(ctx); err != nil {
				log.Warn("due-today notifier stopped", "err", err)
			}
		}()
	}

	server := daemon.NewServer(registry, decider, queue, log)
	if err := server.Serve(ctx, ln); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	log.Info("qid stopped")
	return nil
}

// makeReconciler builds the watcher's OnChange callback: it opens the index,
// runs the existing sync.Reconcile (never dry-run), and logs the outcome. The
// reconcile is idempotent and TOCTOU-guarded, so it is safe to run concurrently
// with a manual `qi sync`.
func makeReconciler(cfg config.Config, log *slog.Logger) watcher.ReconcileFunc {
	return func() {
		idx, err := index.Open()
		if err != nil {
			log.Warn("sync watcher: open index", "err", err)
			return
		}
		defer idx.Close()

		rep, err := sync.Reconcile(cfg, idx, false)
		if err != nil {
			log.Warn("sync watcher: reconcile failed", "err", err)
			return
		}
		// A change-triggered reconcile is usually a no-op (already in sync, or our
		// own write echoing back). Only announce at Info when something actually
		// changed; otherwise keep it at Debug so the log isn't chatty on every save.
		if len(rep.Files) > 0 || len(rep.Conflicts) > 0 || len(rep.Skipped) > 0 {
			log.Info("sync watcher: reconciled",
				"files", len(rep.Files),
				"conflicts", len(rep.Conflicts),
				"skipped", len(rep.Skipped),
			)
		} else {
			log.Debug("sync watcher: reconciled, no changes")
		}
	}
}

// makeDueTodayNotifier builds the scheduler's OnFire callback: it lists open
// tasks, filters to those due/scheduled today, and — if any — sends ONE macOS
// notification summarising them. When nothing is due it stays quiet (logs at
// Debug). It never writes the vault.
func makeDueTodayNotifier(tasksSvc service.TaskService, notifier notify.Notifier, log *slog.Logger) func() {
	return func() {
		tasks, err := tasksSvc.ListOpenTasks()
		if err != nil {
			log.Warn("due-today notifier: list tasks failed", "err", err)
			return
		}
		due := service.FilterDueToday(tasks, time.Now())
		if len(due) == 0 {
			log.Debug("due-today notifier: nothing due today")
			return
		}
		body := dueTodayBody(due)
		if err := notifier.Notify("qi — Due today", body); err != nil {
			log.Warn("due-today notifier: notify failed", "err", err)
		}
	}
}

// dueTodayBody renders the notification body: a count prefix plus the task
// texts joined with "; ", truncated to a sane length with an "…(+N more)"
// suffix so a long list can't produce an unwieldy banner.
func dueTodayBody(tasks []domain.Task) string {
	const maxLen = 200
	prefix := fmt.Sprintf("%d due today: ", len(tasks))

	var b strings.Builder
	b.WriteString(prefix)
	shown := 0
	for i, t := range tasks {
		sep := ""
		if i > 0 {
			sep = "; "
		}
		next := sep + strings.TrimSpace(t.Text)
		if b.Len()+len(next) > maxLen && shown > 0 {
			break
		}
		b.WriteString(next)
		shown++
	}
	if shown < len(tasks) {
		fmt.Fprintf(&b, " …(+%d more)", len(tasks)-shown)
	}
	return b.String()
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

// buildAgendaService wires the shared calendar provider set — calendar.BuildProviders,
// the same builder `qi agenda` uses — into the AgendaService behind the agenda.today
// builtin and the review skills, logging any entry that could not be built.
func buildAgendaService(cfg config.Config, log *slog.Logger) service.AgendaService {
	providers, warnings := calendar.BuildProviders(cfg)
	for _, w := range warnings {
		log.Warn("skipping calendar", "kind", w.Kind, "calendar", w.Name, "err", w.Err)
	}
	return service.AgendaService{Providers: providers}
}
