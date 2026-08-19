// Command qid is the long-running local orchestrator for Qi. It hosts the
// tool registry and exposes a JSON-RPC 2.0 API over a unix-domain socket,
// callable by `qi` and (later) by qi-mcp on behalf of AI clients.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"qi/internal/approval"
	"qi/internal/calendar"
	"qi/internal/config"
	"qi/internal/daemon"
	daemonclient "qi/internal/daemon/client"
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

// walkPatience bounds how long the watcher's vault dir enumeration may run
// before qid flags it Blocked and logs remediation. Ten seconds is orders of
// magnitude past an honest local walk (tens of dirs, stat-only) — exceeding it
// means an open(2) is stuck, not slow.
const walkPatience = 10 * time.Second

// tccRemedy is the operator-facing hint for the launchd/TCC hang (issue #47).
const tccRemedy = "vault dir open is stuck; under launchd this usually means qid lacks the macOS " +
	"Files-and-Folders/Full Disk Access grant for the vault (re-granted binaries lose it on rebuild) — " +
	"grant it in System Settings > Privacy & Security, then restart qid"

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
	if err := registerTools(registry, cfg, log); err != nil {
		return err
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
	// Close unlinks the socket file: net.Listen("unix", …) sets unlinkOnClose,
	// so closing is the whole teardown. Do NOT add an os.Remove here — Serve's
	// ctx.Done goroutine closes the listener early, but this defer runs only
	// after wg.Wait(), mcpMgr.Close() (which waits on MCP stdio subprocesses —
	// seconds), and audit.Close(). By then `qi daemon restart` has already seen
	// the socket go quiet and bound a NEW qid at the same path, and the Remove
	// would delete the successor's socket. A daemon that dies without running
	// this defer at all (SIGKILL) leaves a stale socket file, which the next
	// Listen already handles via its stale-dial probe (internal/daemon/socket.go).
	defer func() { _ = ln.Close() }()

	startedAt := time.Now()
	exePath, err := os.Executable()
	if err != nil {
		log.Warn("cannot resolve own executable path", "err", err)
	}

	// The serve context is the signal context plus one more cancel, so the
	// `daemon.shutdown` RPC ends the daemon through exactly the same path a
	// SIGTERM does: Serve closes the listener (unlinking the socket) and waits
	// for in-flight conns before returning.
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, shutdown := context.WithCancel(signalCtx)
	defer shutdown()

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
		return fmt.Errorf("audit replay: %w", rerr)
	} else if restored := queue.Restore(past); restored.Pending > 0 || restored.Terminal > 0 {
		log.Info("restored approvals from audit log", "pending", restored.Pending, "terminal", restored.Terminal)
	}

	decider := policy.DefaultDecider{}

	log.Info("qid listening", "socket", socketPath, "audit", auditPath, "tools", registry.Len())

	// Opt-in fsnotify-driven auto-reconcile + incremental FTS indexing: when
	// [sync] watch = true, watch the whole vault tree plus the task projection
	// dirs (which may live in other vaults). Task-dir changes run the existing
	// sync reconcile; any main-vault markdown change upserts/deletes its single
	// FTS row so `qi search` stays fresh without a manual `qi index rebuild`
	// (issue #44).
	// The entire setup — including the VaultDirs walk — runs OFF the startup
	// path. A dir open can block indefinitely on this walk (TCC authorization
	// for ~/Documents under launchd, iCloud-evicted dirs), and a wedged watcher
	// must never keep qid from serving RPC. watcherStatus tracks the lifecycle
	// for the `status` RPC, so `qi doctor` can tell a live watcher from one
	// stuck awaiting a privacy grant — the socket dial alone provably cannot
	// (a listening socket accepts from the kernel backlog pre-Serve).
	watcherStatus := watcher.NewStatus()
	if cfg.Sync.Watch {
		watcherStatus.Set(watcher.StateStarting, "enumerating vault dirs")
		go func() {
			taskDirs := watcher.DirsFor(cfg)

			// Enumerate with a patience threshold: past it, flag Blocked and log
			// remediation, then KEEP waiting — the walk completes whenever the
			// grant lands (or the dir materializes), and the watcher then starts.
			walked := make(chan []string, 1)
			go func() { walked <- watcher.VaultDirs(cfg.VaultPath) }()
			var dirs []string
			select {
			case vaultDirs := <-walked:
				dirs = unionDirs(taskDirs, vaultDirs)
			case <-time.After(walkPatience):
				watcherStatus.Set(watcher.StateBlocked, tccRemedy)
				log.Warn("sync watcher: vault dir enumeration blocked", "after", walkPatience, "hint", tccRemedy)
				select {
				case vaultDirs := <-walked:
					dirs = unionDirs(taskDirs, vaultDirs)
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
			if len(dirs) == 0 {
				watcherStatus.Set(watcher.StateFailed, "no directories to watch")
				return
			}

			debounce := time.Duration(cfg.Sync.DebounceMS) * time.Millisecond
			w, werr := watcher.New(watcher.Options{
				Dirs:     dirs,
				Debounce: debounce,
				OnChange: makeOnChange(cfg, taskDirs, log),
				Log:      log,
			})
			if werr != nil {
				watcherStatus.Set(watcher.StateFailed, werr.Error())
				log.Warn("sync watcher: not started", "err", werr)
				return
			}
			watcherStatus.Set(watcher.StateRunning, fmt.Sprintf("watching %d dirs", len(dirs)))
			log.Info("sync watcher enabled", "dirs", dirs, "debounce_ms", cfg.Sync.DebounceMS)
			if err := w.Run(ctx); err != nil {
				watcherStatus.Set(watcher.StateFailed, err.Error())
				log.Warn("sync watcher stopped", "err", err)
			}
		}()
	}

	// Opt-in morning due-today notifier: when [notify] due_today = true, schedule
	// a once-a-day macOS notification (at [notify] at, default 08:00) listing
	// tasks due/scheduled today. Read-only, no policy gate. Off by default.
	if cfg.Notify.DueToday {
		tasksSvc := service.TaskService{
			TaskFilePath: cfg.TaskFilePath,
			TasksDir:     filepath.Dir(cfg.TaskFilePath),
		}
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
	// `status` reports subsystem liveness beyond what a socket dial can prove.
	// Read-only; no policy gate.
	server.Register("status", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		state, detail := watcherStatus.Get()
		return json.Marshal(daemonclient.DaemonStatus{
			Watcher:   daemonclient.WatcherStatus{State: string(state), Detail: detail},
			Exe:       exePath,
			Pid:       os.Getpid(),
			StartedAt: startedAt,
		})
	})
	server.Register("daemon.shutdown", makeShutdownHandler(shutdown, log))
	if err := server.Serve(ctx, ln); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	log.Info("qid stopped")
	return nil
}

// shutdownGrace delays the serve-context cancel so the handler's response is
// written before Serve tears the connection down. It is best-effort only — see
// makeShutdownHandler.
const shutdownGrace = 100 * time.Millisecond

// makeShutdownHandler builds the `daemon.shutdown` RPC: it cancels the serve
// context, which runs the same graceful drain (close listener, wait for
// in-flight conns, unlink socket) as SIGTERM.
//
// It is a control-plane operation, not a vault mutation, so it does NOT route
// through internal/policy or the approval queue — it is hard-denied for every
// caller but "cli". An unset or unrecognized caller is denied, so an AI planner
// or MCP session can never stop the daemon (invariant #3's spirit: non-cli
// callers get no unilateral authority).
//
// Ordering: the cancel runs asynchronously after a short grace so the JSON-RPC
// reply lands first. That is best-effort — ServeConn closes the conn from its
// own ctx.Done goroutine, racing writeResponse — so correctness relies on the
// CLIENT treating a truncated reply to this method as success (see
// client.Shutdown).
func makeShutdownHandler(cancel context.CancelFunc, log *slog.Logger) daemon.Method {
	return func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Caller string `json:"caller"`
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
		}
		if p.Caller != policy.CallerCLI {
			// Log the refusal: nothing else records it (this method bypasses the
			// approval audit log by design), so a non-cli caller repeatedly
			// trying to stop the daemon would otherwise be invisible.
			log.Warn("shutdown refused", "caller", p.Caller)
			return nil, fmt.Errorf("daemon.shutdown is restricted to the cli caller (got %q)", p.Caller)
		}
		log.Info("shutdown requested via rpc")
		go func() {
			time.Sleep(shutdownGrace)
			cancel()
		}()
		return json.Marshal(struct {
			Status string `json:"status"`
		}{Status: "stopping"})
	}
}

// registerTools wires every compiled-in builtin and deterministic skill into
// the registry, in the exact order qid serves them. run() and the anti-drift
// test (TestRegisteredToolsDeclareMutationExplicitly) both call it, so the test
// exercises precisely the tool set qid exposes — there is no second copy of the
// wiring to drift out of sync. When adding a tool, register it HERE and classify
// it in expectedMutating (main_test.go): a vault-writing tool MUST declare
// Mutating: true so internal/policy routes non-cli callers through the approval
// queue (invariant #3).
func registerTools(registry *tools.Registry, cfg config.Config, log *slog.Logger) error {
	tasksSvc := service.TaskService{
		TaskFilePath: cfg.TaskFilePath,
		TasksDir:     filepath.Dir(cfg.TaskFilePath),
	}
	agendaSvc := buildAgendaService(cfg, log)
	notesSvc := service.NoteService{NotesDir: cfg.NotesPath}
	inboxArchive := filepath.Join(cfg.InboxPath, "archive")

	if err := builtin.RegisterCapture(registry, cfg.InboxPath); err != nil {
		return fmt.Errorf("register capture: %w", err)
	}
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
	return nil
}

// unionDirs merges dir lists into a unique sorted set.
func unionDirs(lists ...[]string) []string {
	seen := make(map[string]struct{})
	for _, l := range lists {
		for _, d := range l {
			seen[d] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// makeOnChange builds the watcher's OnChange callback. Two jobs, gated by
// which paths changed:
//
//   - a change in a task dir runs the existing sync.Reconcile (never dry-run) —
//     gating matters because reconcile reads every canon+projection task file
//     across ALL vaults, and a mere note save must not trigger reads of other
//     (possibly iCloud-evicted) vaults' files;
//   - every changed markdown path inside the MAIN vault gets its single FTS
//     row upserted (or deleted, when the path is gone). Only main-vault paths:
//     projection files in client vaults are outside Rebuild's walk, and rows
//     Rebuild wouldn't recreate must not be planted in the index.
//
// The reconcile is idempotent and TOCTOU-guarded, so it is safe to run
// concurrently with a manual `qi sync`. The FTS upsert reads only files that
// just produced change events — present on disk, never an eviction stall.
func makeOnChange(cfg config.Config, taskDirs []string, log *slog.Logger) watcher.OnChangeFunc {
	inTaskDir := make(map[string]bool, len(taskDirs))
	for _, d := range taskDirs {
		inTaskDir[d] = true
	}
	vaultPrefix := cfg.VaultPath + string(os.PathSeparator)

	return func(paths []string) {
		idx, err := index.Open()
		if err != nil {
			log.Warn("sync watcher: open index", "err", err)
			return
		}
		defer idx.Close()

		needReconcile := false
		indexed, dropped := 0, 0
		for _, p := range paths {
			if inTaskDir[filepath.Dir(p)] {
				needReconcile = true
			}
			if !strings.HasPrefix(p, vaultPrefix) {
				continue
			}
			if _, statErr := os.Stat(p); statErr == nil {
				if err := idx.UpsertFile(p); err != nil {
					log.Warn("sync watcher: index upsert failed", "path", p, "err", err)
				} else {
					indexed++
				}
			} else {
				if err := idx.DeleteFile(p); err != nil {
					log.Warn("sync watcher: index delete failed", "path", p, "err", err)
				} else {
					dropped++
				}
			}
		}
		if indexed > 0 || dropped > 0 {
			log.Debug("sync watcher: index updated", "upserted", indexed, "deleted", dropped)
		}

		if !needReconcile {
			return
		}
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
