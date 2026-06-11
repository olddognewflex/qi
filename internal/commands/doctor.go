package commands

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/daemon"
	"qi/internal/index"
	"qi/internal/remotequeue"
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
		Short: "Run health checks (config, vault, qid, index, worker)",
		Long: "Inspects the local qi setup and reports the health of each component:\n" +
			"config file, vault directories, the qid socket, the search index, and\n" +
			"(when enabled) cloud-queue Worker reachability. Read-only — it mutates\n" +
			"nothing. Exits non-zero if any check fails.",
		Args:         cobra.NoArgs,
		SilenceUsage: true, // a failed check is not a usage error
		RunE: func(cmd *cobra.Command, args []string) error {
			rep := &report{w: cmd.OutOrStdout()}

			checkConfig(rep, cfg)
			checkVault(rep, cfg)
			checkSocket(rep)
			checkIndex(rep, cfg)
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

func checkSocket(rep *report) {
	sockPath, err := daemon.SocketPath()
	if err != nil {
		rep.check(statusWarn, "qid socket", "cannot resolve path")
		rep.detail("%v", err)
		return
	}
	if _, err := os.Stat(sockPath); err != nil {
		rep.check(statusWarn, "qid socket", "daemon not running")
		rep.detail("no socket at %s (start qid)", sockPath)
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

func checkIndex(rep *report, cfg config.Config) {
	dbPath := index.DBPath()
	fi, err := os.Stat(dbPath)
	if err != nil {
		rep.check(statusWarn, "index", "not built")
		rep.detail("run qi index rebuild")
		return
	}
	newest, count := newestMarkdown(cfg.VaultPath)
	switch {
	case count == 0:
		rep.check(statusOK, "index", "no markdown files to index")
	case newest.After(fi.ModTime()):
		rep.check(statusWarn, "index", "stale")
		rep.detail("vault changed since last rebuild; run qi index rebuild")
	default:
		rep.check(statusOK, "index", fmt.Sprintf("fresh (%d files indexed)", count))
	}
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
