// Package watcher implements qid's opt-in fsnotify-driven auto-reconcile. When
// [sync] watch is enabled, qid watches the vault's task directories and invokes
// an injected callback (debounced) whenever a markdown file changes, eliminating
// manual `qi sync` runs.
//
// The watcher itself stays decoupled from the reconcile logic: it depends only
// on fsnotify, config, and the standard library. The actual index-open +
// sync.Reconcile + logging lives in the daemon's OnChange closure, which keeps
// this package testable and respects the layering (cmd wires sync/index; the
// watcher never imports them).
//
// Directories are watched, not individual files: editors (and Obsidian Sync)
// rename-on-save, so file-level watches miss writes. A directory watch plus a
// ".md" filter is robust against that.
package watcher

import (
	"context"
	"errors"
	"log/slog"
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

// ReconcileFunc is the injected callback the watcher invokes (debounced) when a
// watched markdown file changes. The daemon's closure does the actual
// index-open + sync.Reconcile + logging.
type ReconcileFunc func()

// Watcher watches a set of directories and fires onChange (debounced) on markdown
// writes within them.
type Watcher struct {
	dirs     []string
	debounce time.Duration
	onChange ReconcileFunc
	log      *slog.Logger
}

// Options configures New.
type Options struct {
	Dirs     []string
	Debounce time.Duration
	OnChange ReconcileFunc
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
// cancelled. A markdown Write/Create/Rename event (re)arms a single debounce
// timer; when it fires, onChange runs once. Bursts coalesce because every event
// resets the timer. onChange runs synchronously in the select loop, so events
// arriving during a long reconcile queue in the fsnotify channel and re-arm the
// timer afterward rather than being dropped.
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
			if !isReconcileEvent(event) {
				continue
			}
			arm()

		case <-timerC:
			w.onChange()

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("sync watcher: fsnotify error", "err", err)
		}
	}
}

// isReconcileEvent reports whether an fsnotify event is a markdown
// Write/Create/Rename that should trigger a reconcile.
func isReconcileEvent(event fsnotify.Event) bool {
	if !event.Op.Has(fsnotify.Write) &&
		!event.Op.Has(fsnotify.Create) &&
		!event.Op.Has(fsnotify.Rename) {
		return false
	}
	return strings.HasSuffix(event.Name, ".md")
}

// DirsFor returns the unique, sorted set of directories qid must watch: the
// canon task dir (filepath.Dir(cfg.TaskFilePath)) plus the dir of each project
// projection file.
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
