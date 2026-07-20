package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/ai"
	"qi/internal/config"
	"qi/internal/daemon"
	"qi/internal/daemon/client"
	"qi/internal/embed"
	"qi/internal/index"
	"qi/internal/remotequeue"
	"qi/internal/watcher"
)

// checkStatus is the outcome of a single doctor check. Only statusFail makes
// `qi doctor` exit non-zero; statusWarn flags optional/lazy components that are
// not yet present but are not errors.
type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

func (s checkStatus) label() string {
	switch s {
	case statusOK:
		return "ok  "
	case statusWarn:
		return "warn"
	default:
		return "fail"
	}
}

// report accumulates check results to a writer and counts failures so the
// command can return a non-zero exit when any check fails.
type report struct {
	w        io.Writer
	failures int
}

func (r *report) check(status checkStatus, name, summary string) {
	fmt.Fprintf(r.w, "[%s] %s", status.label(), name)
	if summary != "" {
		fmt.Fprintf(r.w, " — %s", summary)
	}
	fmt.Fprintln(r.w)
	if status == statusFail {
		r.failures++
	}
}

func (r *report) detail(format string, args ...any) {
	fmt.Fprintf(r.w, "       "+format+"\n", args...)
}

func newDoctorCommand(cfg config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run health checks (config, vault, qid, index, ai model, worker)",
		Long: "Inspects the local qi setup and reports the health of each component:\n" +
			"config file, vault directories, iCloud-evicted vault files (macOS), the\n" +
			"qid socket, the search index, the resolved AI model, and (when enabled)\n" +
			"cloud-queue Worker reachability. Read-only — it mutates nothing. Exits\n" +
			"non-zero if any check fails.",
		Args:         cobra.NoArgs,
		SilenceUsage: true, // a failed check is not a usage error
		RunE: func(cmd *cobra.Command, args []string) error {
			rep := &report{w: cmd.OutOrStdout()}

			checkConfig(rep, cfg)
			checkVault(rep, cfg)
			checkDataless(rep, cfg)
			checkSocket(rep)
			checkDaemon(cmd.Context(), rep, cfg)
			checkIndex(rep, cfg)
			checkEmbeddings(rep, cfg)
			checkAIModel(rep, cfg)
			checkWorker(cmd.Context(), rep, cfg)

			fmt.Fprintln(rep.w)
			if rep.failures > 0 {
				return fmt.Errorf("%d check(s) failed", rep.failures)
			}
			fmt.Fprintln(rep.w, "all checks passed")
			return nil
		},
	}
	return cmd
}

func checkConfig(rep *report, cfg config.Config) {
	cfgPath := config.ConfigPath()
	if _, err := os.Stat(cfgPath); err == nil {
		rep.check(statusOK, "config", cfgPath)
	} else {
		rep.check(statusWarn, "config", "no config file; using env/defaults")
		rep.detail("expected at %s", cfgPath)
	}
	rep.detail("vault_path:    %s", cfg.VaultPath)
	rep.detail("task_file:     %s", cfg.TaskFilePath)
}

func checkVault(rep *report, cfg config.Config) {
	if fi, err := os.Stat(cfg.VaultPath); err != nil || !fi.IsDir() {
		rep.check(statusFail, "vault path", cfg.VaultPath)
		if err != nil {
			rep.detail("%v", err)
		} else {
			rep.detail("not a directory")
		}
		return
	}
	rep.check(statusOK, "vault path", cfg.VaultPath)

	subdirs := []struct {
		name string
		path string
	}{
		{"inbox", cfg.InboxPath},
		{"tasks", filepath.Dir(cfg.TaskFilePath)},
		{"notes", cfg.NotesPath},
		{"daily", cfg.DailyPath},
	}
	var missing []string
	for _, s := range subdirs {
		if fi, err := os.Stat(s.path); err != nil || !fi.IsDir() {
			missing = append(missing, s.name)
		}
	}
	if len(missing) == 0 {
		rep.check(statusOK, "vault subdirs", "inbox, tasks, notes, daily present")
	} else {
		rep.check(statusWarn, "vault subdirs", "missing: "+strings.Join(missing, ", "))
		rep.detail("created lazily on first write")
	}
}

// checkDataless reports vault files whose contents macOS has evicted to iCloud.
// It is macOS-only (see dataless_other.go) and stat-only: it must never read a
// vault file, because that read is what triggers the slow re-download it warns
// about.
func checkDataless(rep *report, cfg config.Config) {
	if !datalessSupported {
		return
	}
	scan := scanDataless(cfg.VaultPath, fileBlocks)
	status, summary := datalessStatus(scan)
	rep.check(status, "vault data", summary)
	if status == statusWarn {
		rep.detail("contents evicted by iCloud; reads re-download and can stall or fail offline")
		rep.detail("%s", datalessRemedy)
	}
}

func checkSocket(rep *report) {
	sockPath, err := daemon.SocketPath()
	if err != nil {
		rep.check(statusWarn, "qid socket", "cannot resolve path")
		rep.detail("%v", err)
		return
	}
	if _, err := os.Stat(sockPath); err != nil {
		rep.check(statusWarn, "qid socket", "daemon not running")
		rep.detail("no socket at %s (run qi daemon start)", sockPath)
		return
	}
	conn, err := net.DialTimeout("unix", sockPath, 200*time.Millisecond)
	if err != nil {
		rep.check(statusWarn, "qid socket", "stale socket")
		rep.detail("present but dial failed: %v", err)
		return
	}
	_ = conn.Close()
	rep.check(statusOK, "qid socket", sockPath)
}

// checkDaemon asks a live qid what state it is in: whether its vault watcher
// actually started, and whether it is still running the qid binary on disk.
// The socket dial in checkSocket cannot prove either: a listening socket
// accepts connections from the kernel backlog even when the daemon is wedged
// pre-Serve, a healthy daemon can still have a watcher stuck awaiting a
// macOS privacy grant (issue #47), and a daemon rebuilt underneath keeps
// serving the old code silently. Skipped silently when no daemon is
// reachable — checkSocket already reported that.
func checkDaemon(ctx context.Context, rep *report, cfg config.Config) {
	sockPath, err := daemon.SocketPath()
	if err != nil {
		return
	}
	if _, err := os.Stat(sockPath); err != nil {
		return
	}
	cl, err := client.Dial(sockPath, 200*time.Millisecond)
	if err != nil {
		return
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := cl.Status(ctx)
	if err != nil {
		var ce *client.CallError
		if errors.As(err, &ce) && ce.Code == daemon.CodeMethodNotFound {
			rep.check(statusWarn, "qid watcher", "unknown (daemon predates status RPC)")
			rep.detail("rebuild and restart qid")
			return
		}
		// A daemon wedged pre-Serve accepts the dial but never answers: the
		// 2s deadline turns that silence into a diagnosis.
		rep.check(statusWarn, "qid watcher", "daemon accepted connection but did not answer")
		rep.detail("%v", err)
		rep.detail("qid may be wedged before Serve (see issue #47); restart it and check ~/Library/Logs/qid.log")
		return
	}

	switch st.Watcher.State {
	case string(watcher.StateRunning):
		rep.check(statusOK, "qid watcher", st.Watcher.Detail)
	case string(watcher.StateDisabled):
		rep.check(statusOK, "qid watcher", "disabled ([sync] watch not set)")
	case string(watcher.StateBlocked):
		rep.check(statusWarn, "qid watcher", "blocked")
		rep.detail("%s", st.Watcher.Detail)
	case string(watcher.StateStarting):
		rep.check(statusWarn, "qid watcher", "still starting")
		rep.detail("%s", st.Watcher.Detail)
	default: // failed or unknown
		rep.check(statusWarn, "qid watcher", string(st.Watcher.State))
		rep.detail("%s", st.Watcher.Detail)
	}

	// A daemon running code that no longer exists on disk warns, never fails:
	// it is environmental drift the user resolves at their own pace. An unknown
	// verdict (old daemon, unlocatable binary) also warns rather than passing
	// silently, matching the watcher branches above — warn never affects the
	// exit code, so the doctor contract is unchanged either way.
	staleness, summary := daemonStaleness(st.Exe, "", st.StartedAt)
	if staleness == daemon.StalenessCurrent {
		rep.check(statusOK, "qid binary", summary)
		return
	}
	rep.check(statusWarn, "qid binary", summary)
}

func checkIndex(rep *report, cfg config.Config) {
	dbPath := index.DBPath()
	if _, err := os.Stat(dbPath); err != nil {
		rep.check(statusWarn, "index", "not built")
		rep.detail("run qi index rebuild")
		return
	}
	// Freshness compares vault file mtimes against the index's last_indexed
	// marker — NOT the db file's mtime, which task-sync writes bump without
	// touching the FTS table (issue #44). Stats opens read-only, keeping this
	// check genuinely mutation-free.
	lastIndexed, rows, err := index.Stats()
	if err != nil {
		rep.check(statusWarn, "index", "unreadable")
		rep.detail("%v", err)
		return
	}
	newest, files := newestMarkdown(cfg.VaultPath)
	switch {
	case files == 0:
		rep.check(statusOK, "index", "no markdown files to index")
	case lastIndexed.IsZero():
		rep.check(statusWarn, "index", "freshness unknown (index predates tracking)")
		rep.detail("run qi index rebuild")
	case newest.After(lastIndexed):
		rep.check(statusWarn, "index", "stale")
		rep.detail("vault changed since last index write; run qi index rebuild")
	default:
		rep.check(statusOK, "index", fmt.Sprintf("fresh (%d notes indexed, %d files on disk)", rows, files))
	}
}

// checkEmbeddings reports the freshness of the opt-in semantic index. The
// watcher keeps FTS current per-file but never re-embeds (embedding is a network
// call to Ollama — deliberately kept off the debounced hot path), so an edited
// note's vector goes stale until the next manual `qi embed`. That drift is
// otherwise invisible (checkIndex only tracks FTS); this surfaces it. It also
// flags a model change (vectors built with a different model than search will
// query with), the companion to SemanticSearch's hard dim-mismatch reject.
// Warn-only: stale/absent embeddings are an opt-in feature the user rebuilds at
// their own pace, never a failed install.
func checkEmbeddings(rep *report, cfg config.Config) {
	if !cfg.Embeddings.Enabled {
		rep.check(statusOK, "embeddings", "disabled ([embeddings] enabled not set)")
		return
	}
	if _, err := os.Stat(index.DBPath()); err != nil {
		rep.check(statusWarn, "embeddings", "not built")
		rep.detail("run qi embed")
		return
	}
	count, newest, models, err := index.EmbeddingStats()
	if err != nil {
		rep.check(statusWarn, "embeddings", "unreadable")
		rep.detail("%v", err)
		return
	}
	if count == 0 {
		rep.check(statusWarn, "embeddings", "enabled but none built")
		rep.detail("run qi embed")
		return
	}

	wantModel := cfg.Embeddings.Model
	if wantModel == "" {
		wantModel = embed.DefaultModel
	}
	if other := modelsOtherThan(models, wantModel); len(other) > 0 {
		rep.check(statusWarn, "embeddings", fmt.Sprintf("built with %s, config wants %s", strings.Join(other, ", "), wantModel))
		rep.detail("embed model changed since last build; run qi embed to rebuild (search errors on the dim mismatch otherwise)")
		return
	}

	newestNote, _ := newestMarkdown(cfg.VaultPath)
	switch {
	case newest.IsZero():
		rep.check(statusWarn, "embeddings", "freshness unknown (no timestamp)")
		rep.detail("run qi embed")
	case newestNote.After(newest):
		rep.check(statusWarn, "embeddings", "stale")
		rep.detail("vault changed since last embed; incremental indexing does not re-embed — run qi embed")
	default:
		rep.check(statusOK, "embeddings", fmt.Sprintf("fresh (%d vectors, model %s)", count, wantModel))
	}
}

// modelsOtherThan returns the models in the list that are not want (the
// configured embed model). Non-empty means the stored vectors were built with a
// different model than search will query with.
func modelsOtherThan(models []string, want string) []string {
	var out []string
	for _, m := range models {
		if m != want {
			out = append(out, m)
		}
	}
	return out
}

// newestMarkdown returns the latest modification time across markdown files in
// the vault and their count. Hidden directories (dotfiles) are skipped. It only
// stats files — it never reads their contents — so it stays cheap.
func newestMarkdown(root string) (time.Time, int) {
	var newest time.Time
	count := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		count++
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest, count
}

// checkAIModel reports the AI model `qi ai run` would resolve for its
// primary provider, without constructing a provider client or making any
// network call — purely config inspection plus each provider's own
// built-in default (the same ones internal/ai.Generate falls back to when
// GenerateRequest.Model is empty). It warns, never fails: a stale or empty
// model id is a config nit the user can fix at their own pace, not a
// broken install. Issue #55.
func checkAIModel(rep *report, cfg config.Config) {
	provider, model, note := resolveAIModel(cfg)
	if strings.TrimSpace(model) == "" {
		rep.check(statusWarn, "ai model", fmt.Sprintf("no model configured for provider %q", provider))
		rep.detail("set [ai].model (or a model on each [[ai.providers]] entry)")
		return
	}
	summary := fmt.Sprintf("%s (provider: %s)", model, provider)
	if note != "" {
		summary += " — " + note
	}
	rep.check(statusOK, "ai model", summary)
}

// resolveAIModel mirrors buildLLM's provider/model precedence (see ai.go)
// purely for reporting: it never constructs a provider or performs I/O.
// When an [[ai.providers]] failover chain is configured, it reports on the
// chain's primary (first) entry — the one `qi ai run` tries first. Returns
// the resolved provider name, its model id (empty when the provider
// requires an explicit model but none is configured), and an optional note.
func resolveAIModel(cfg config.Config) (provider, model, note string) {
	if len(cfg.AI.Providers) > 0 {
		pc := cfg.AI.Providers[0]
		prov, err := ai.ParseProvider(pc.Provider)
		if err != nil || prov == "" {
			return pc.Provider, "", fmt.Sprintf("primary of %d chained providers; unrecognized provider name", len(cfg.AI.Providers))
		}
		note = fmt.Sprintf("primary of %d chained providers", len(cfg.AI.Providers))
		return string(prov), defaultAIModel(prov, pc.Model), note
	}

	prov, err := ai.ParseProvider(cfg.AI.Provider)
	if err != nil || prov == "" {
		prov = ai.ProviderAnthropic
	}
	if envProvider := os.Getenv("QI_AI_PROVIDER"); envProvider != "" {
		if p, err := ai.ParseProvider(envProvider); err == nil && p != "" {
			prov = p
		}
	}

	configured := cfg.AI.Model
	if prov == ai.ProviderOllama {
		configured = cfg.AI.OllamaModel
	}
	return string(prov), defaultAIModel(prov, configured), ""
}

// defaultAIModel applies a provider's own built-in fallback for an unset
// model, matching AnthropicProvider.Generate / OllamaProvider.Generate.
// The OpenAI-compatible providers (openai/kimi/opencode/zai) have no
// built-in default — a model is required — so an empty configured value
// stays empty.
func defaultAIModel(prov ai.Provider, configured string) string {
	if configured != "" {
		return configured
	}
	switch prov {
	case ai.ProviderAnthropic:
		return ai.DefaultAnthropicModel
	case ai.ProviderOllama:
		return ai.DefaultOllamaModel
	default:
		return ""
	}
}

func checkWorker(ctx context.Context, rep *report, cfg config.Config) {
	switch {
	case !cfg.RemoteQueue.Enabled:
		rep.check(statusOK, "worker", "remote queue disabled (skipped)")
		return
	case cfg.RemoteQueue.URL == "":
		rep.check(statusFail, "worker", "enabled but url empty")
		rep.detail("set [remote_queue].url or QI_QUEUE_URL")
		return
	case cfg.RemoteQueue.Token == "":
		rep.check(statusFail, "worker", "enabled but token empty")
		rep.detail("set [remote_queue].token or QI_QUEUE_TOKEN")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := remotequeue.NewClient(cfg.RemoteQueue.URL, cfg.RemoteQueue.Token)
	if _, err := client.Pull(ctx, 1); err != nil {
		rep.check(statusFail, "worker", "unreachable")
		rep.detail("%v", err)
		return
	}
	rep.check(statusOK, "worker", cfg.RemoteQueue.URL)
}
