// Package watcher implements qid's opt-in fsnotify-driven vault watching. When
// [sync] watch is enabled, qid watches the vault (and the task projection
// dirs, which may live in other vaults) and invokes an injected callback
// (debounced, with the changed paths) whenever a markdown file changes —
// driving both the task auto-reconcile and incremental FTS index updates.
//
// The watcher itself stays decoupled from the reconcile/index logic: it
// depends only on fsnotify, config, and the standard library. The actual
// index-open + sync.Reconcile + FTS upserts + logging live in the daemon's
// OnChange closure, which keeps this package testable and respects the
// layering (cmd wires sync/index; the watcher never imports them).
//
// Directories are watched, not individual files: editors (and Obsidian Sync)
// rename-on-save, so file-level watches miss writes. A directory watch plus a
// ".md" filter is robust against that. fsnotify is non-recursive, so the
// vault's directory tree is enumerated up front (VaultDirs) and directories
// created while running are added to the watch as their Create events arrive.
package watcher

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"qi/internal/config"
)

// DefaultDebounce is applied when Options.Debounce is non-positive. It coalesces
// the burst of events a single save (or a sync sweep) produces.
const DefaultDebounce = 750 * time.Millisecond

// OnChangeFunc is the injected callback the watcher invokes (debounced) with
// the set of markdown paths that changed since the last invocation, sorted.
// A path may no longer exist (Remove/Rename) — the callback decides whether
// that means an index delete. The daemon's closure does the actual
// index-open + sync.Reconcile + FTS upsert + logging.
type OnChangeFunc func(paths []string)

// Watcher watches a set of directories and fires onChange (debounced) with the
// changed markdown paths within them.
type Watcher struct {
	dirs     []string
	debounce time.Duration
	onChange OnChangeFunc
	log      *slog.Logger
}

// Options configures New.
type Options struct {
	Dirs     []string
	Debounce time.Duration
	OnChange OnChangeFunc
	Log      *slog.Logger
}

// New constructs a Watcher. It errors when OnChange is nil or Dirs is empty.
// A non-positive Debounce defaults to DefaultDebounce; a nil Log defaults to
// slog.Default().
func New(opts Options) (*Watcher, error) {
	if opts.OnChange == nil {
		return nil, errors.New("watcher: OnChange is required")
	}
	if len(opts.Dirs) == 0 {
		return nil, errors.New("watcher: at least one dir is required")
	}
	debounce := opts.Debounce
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{
		dirs:     opts.Dirs,
		debounce: debounce,
		onChange: opts.OnChange,
		log:      log,
	}, nil
}

// Run creates an fsnotify watcher, adds each dir, and loops until ctx is
// cancelled. A markdown Write/Create/Rename/Remove event records the path and
// (re)arms a single debounce timer; when it fires, onChange runs once with the
// accumulated paths. Bursts coalesce because every event resets the timer.
// onChange runs synchronously in the select loop, so events arriving during a
// long reconcile queue in the fsnotify channel and re-arm the timer afterward
// rather than being dropped. A Create of a new directory adds it to the watch
// (fsnotify is non-recursive).
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = fsw.Close() }()

	for _, dir := range w.dirs {
		if err := fsw.Add(dir); err != nil {
			// A project vault may be offline; log and skip rather than aborting
			// the whole watcher.
			w.log.Warn("sync watcher: skipping dir", "dir", dir, "err", err)
			continue
		}
	}

	// timer is lazily created on the first qualifying event; nil means disarmed.
	var timer *time.Timer
	var timerC <-chan time.Time

	arm := func() {
		if timer == nil {
			timer = time.NewTimer(w.debounce)
			timerC = timer.C
			return
		}
		// Drain a value that fired but hasn't been received yet, so a stale tick
		// can't trigger an extra reconcile after Reset.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(w.debounce)
	}

	// pending accumulates changed paths between debounce fires.
	pending := make(map[string]struct{})

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			return nil

		case event, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			// A newly created directory must be watched for the files that will
			// land in it (fsnotify is non-recursive). Hidden dirs stay ignored.
			if event.Op.Has(fsnotify.Create) && !strings.HasPrefix(filepath.Base(event.Name), ".") {
				if fi, err := os.Stat(event.Name); err == nil && fi.IsDir() {
					if err := fsw.Add(event.Name); err != nil {
						w.log.Warn("sync watcher: cannot watch new dir", "dir", event.Name, "err", err)
					}
					continue
				}
			}
			if !isChangeEvent(event) {
				continue
			}
			pending[event.Name] = struct{}{}
			arm()

		case <-timerC:
			paths := make([]string, 0, len(pending))
			for p := range pending {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			clear(pending)
			w.onChange(paths)

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("sync watcher: fsnotify error", "err", err)
		}
	}
}

// isChangeEvent reports whether an fsnotify event is a markdown
// Write/Create/Rename/Remove that should be recorded and trigger the
// (debounced) onChange. Remove and Rename matter for the FTS index: a
// vanished path must drop its row or search returns ghosts.
func isChangeEvent(event fsnotify.Event) bool {
	if !event.Op.Has(fsnotify.Write) &&
		!event.Op.Has(fsnotify.Create) &&
		!event.Op.Has(fsnotify.Rename) &&
		!event.Op.Has(fsnotify.Remove) {
		return false
	}
	return strings.HasSuffix(event.Name, ".md")
}

// DirsFor returns the unique, sorted set of task directories qid must watch
// for the sync reconcile: the canon task dir (filepath.Dir(cfg.TaskFilePath))
// plus the dir of each project projection file.
func DirsFor(cfg config.Config) []string {
	seen := make(map[string]struct{})
	add := func(dir string) {
		if dir == "" {
			return
		}
		seen[dir] = struct{}{}
	}

	add(filepath.Dir(cfg.TaskFilePath))
	for _, pv := range cfg.Projects {
		add(filepath.Dir(pv.File))
	}

	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

// VaultDirs enumerates every directory under root (root included), skipping
// hidden dirs (.git, .obsidian, ...). This is the watch set for incremental
// FTS indexing: the FTS table covers ALL vault markdown (vault.WalkVault),
// including dirs qi doesn't own like 40-projects/, so the whole tree must be
// watched — not just the derived subdirs. Stat-only; never reads file
// contents (iCloud-evicted files must not be touched here).
func VaultDirs(root string) []string {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	sort.Strings(dirs)
	return dirs
}
