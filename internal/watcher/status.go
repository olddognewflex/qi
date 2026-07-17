package watcher

import "sync"

// State names the watcher's lifecycle phase. String-typed so it serializes
// directly into the qid `status` RPC.
type State string

const (
	// StateDisabled — [sync] watch is off; no watcher was requested.
	StateDisabled State = "disabled"
	// StateStarting — setup begun (dir enumeration in progress).
	StateStarting State = "starting"
	// StateBlocked — dir enumeration exceeded its patience threshold and is
	// still in flight. On macOS under launchd this is the signature of a
	// missing Files-and-Folders/Full Disk Access grant (issue #47): the
	// open(2) blocks awaiting tccd, and a background agent can show no prompt.
	StateBlocked State = "blocked"
	// StateRunning — the watcher is live and events are being processed.
	StateRunning State = "running"
	// StateFailed — setup or the run loop returned an error; detail says why.
	StateFailed State = "failed"
)

// Status is a concurrency-safe record of the watcher's lifecycle, written by
// qid's watcher setup goroutine and read by the `status` RPC (any connection
// goroutine). The zero value is not useful — use NewStatus, which starts
// Disabled.
type Status struct {
	mu     sync.Mutex
	state  State
	detail string
}

// NewStatus returns a Status in StateDisabled.
func NewStatus() *Status {
	return &Status{state: StateDisabled}
}

// Set records a new state with a human-readable detail.
func (s *Status) Set(state State, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
	s.detail = detail
}

// Get returns the current state and detail.
func (s *Status) Get() (State, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.detail
}
