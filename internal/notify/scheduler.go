package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// DefaultAt is the time-of-day the due-today notifier fires when Options.At is
// empty.
const DefaultAt = "08:00"

// DailyScheduler invokes OnFire once per day at a fixed local time-of-day. It is
// decoupled from what the callback does: cmd/qid injects a closure that lists
// due-today tasks and sends a notification.
type DailyScheduler struct {
	hour   int
	min    int
	onFire func()
	now    func() time.Time
	log    *slog.Logger
}

// Options configures New.
type Options struct {
	At     string // "HH:MM" (24h); empty defaults to DefaultAt
	OnFire func()
	Now    func() time.Time // injectable clock; defaults to time.Now
	Log    *slog.Logger
}

// New constructs a DailyScheduler. It errors when OnFire is nil or At is set but
// not a valid "HH:MM". An empty At defaults to DefaultAt; a nil Now defaults to
// time.Now and a nil Log to slog.Default().
func New(opts Options) (*DailyScheduler, error) {
	if opts.OnFire == nil {
		return nil, errors.New("notify: OnFire is required")
	}
	at := opts.At
	if at == "" {
		at = DefaultAt
	}
	hour, min, err := parseHHMM(at)
	if err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &DailyScheduler{
		hour:   hour,
		min:    min,
		onFire: opts.OnFire,
		now:    now,
		log:    log,
	}, nil
}

// Run schedules and invokes OnFire once at the configured time each day until
// ctx is cancelled. On each iteration it waits until the next occurrence of the
// at-time (today if still ahead, else tomorrow), fires synchronously, then loops
// to schedule the next day. It returns nil on a clean cancellation, mirroring
// watcher.Run.
func (s *DailyScheduler) Run(ctx context.Context) error {
	for {
		now := s.now()
		wait := nextFire(now, s.hour, s.min).Sub(now)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		case <-timer.C:
			s.onFire()
		}
	}
}

// nextFire returns the next local time at hour:min strictly after now: the same
// day when now is before hour:min, otherwise the next day.
func nextFire(now time.Time, hour, min int) time.Time {
	candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())
	if !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

// parseHHMM parses a 24h "HH:MM" time-of-day into hour and minute, rejecting
// malformed input and out-of-range values.
func parseHHMM(s string) (hour, min int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("notify: invalid time %q (want HH:MM)", s)
	}
	if len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, 0, fmt.Errorf("notify: invalid time %q (want HH:MM)", s)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("notify: invalid hour in %q: %w", s, err)
	}
	min, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("notify: invalid minute in %q: %w", s, err)
	}
	if hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("notify: hour out of range in %q", s)
	}
	if min < 0 || min > 59 {
		return 0, 0, fmt.Errorf("notify: minute out of range in %q", s)
	}
	return hour, min, nil
}
