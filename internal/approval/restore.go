package approval

import "bytes"

// RestoreStats reports how many live and lookup-only entries replay restored.
type RestoreStats struct {
	Pending  int
	Terminal int
}

// Restore rebuilds live pending entries and lookup-only terminal history from
// a replayed audit log. Approved entries without a durable terminal outcome
// are restored as pending and require another explicit approval. It never
// executes tools or emits new audit events.
func (q *Queue) Restore(entries []AuditEntry) RestoreStats {
	type accumulator struct {
		enqueue *AuditEntry
		latest  AuditEntry
	}
	byID := make(map[string]*accumulator)
	order := make([]string, 0)
	for i := range entries {
		e := entries[i]
		if e.ID == "" {
			continue
		}
		a, ok := byID[e.ID]
		if !ok {
			a = &accumulator{}
			byID[e.ID] = a
			order = append(order, e.ID)
		}
		if e.Event == EventEnqueue {
			copy := e
			a.enqueue = &copy
		}
		a.latest = e
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	stats := RestoreStats{}
	for _, id := range order {
		a := byID[id]
		if a.enqueue == nil || q.hasID(id) {
			continue
		}
		p := &Pending{
			ID: id, Caller: a.enqueue.Caller, CallID: a.enqueue.CallID,
			ToolName: a.enqueue.Tool, Params: cloneRaw(a.enqueue.Params),
			Status: StatusPending, Reason: a.enqueue.Reason, CreatedAt: a.enqueue.Time,
		}
		switch a.latest.Event {
		case EventDeny, EventExecute, EventFail:
			outcome := a.latest.Outcome
			if outcome == nil || !terminalOutcomeMatches(a.latest.Event, outcome.Status) ||
				!terminalAuditMatchesEnqueue(a.latest, *a.enqueue) {
				continue
			}
			p.Status, p.Result, p.Reason, p.Err = outcome.Status, cloneRaw(outcome.Result), outcome.Reason, outcome.Err
			p.DecidedAt, p.ExecutedAt = cloneTime(outcome.DecidedAt), cloneTime(outcome.ExecutedAt)
			q.history[id] = p
			stats.Terminal++
		case EventEnqueue, EventApprove:
			q.items[id] = p
			stats.Pending++
		}
	}
	return stats
}

func terminalAuditMatchesEnqueue(terminal, enqueue AuditEntry) bool {
	return terminal.Caller == enqueue.Caller &&
		terminal.CallID == enqueue.CallID &&
		terminal.Tool == enqueue.Tool &&
		bytes.Equal(terminal.Params, enqueue.Params)
}

func (q *Queue) hasID(id string) bool {
	if _, exists := q.items[id]; exists {
		return true
	}
	_, exists := q.history[id]
	return exists
}

func terminalOutcomeMatches(event AuditEvent, status Status) bool {
	switch event {
	case EventDeny:
		return status == StatusDenied
	case EventExecute:
		return status == StatusExecuted
	case EventFail:
		return status == StatusFailed
	default:
		return false
	}
}
