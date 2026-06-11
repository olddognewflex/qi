package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"qi/internal/remotequeue"
	"qi/internal/service"
	"qi/internal/vault"
)

const drainToken = "drain-token"

// fakeWorker is an in-memory stand-in for the Cloudflare Worker queue. Each test
// configures the rows it serves on /pull and inspects the ack/deadletter calls
// it receives. pullBatches lets a test serve different rows on successive pulls
// (e.g. re-drain after a dropped ack).
type fakeWorker struct {
	mu          sync.Mutex
	pullBatches [][]remotequeue.Task // one batch per pull; last batch repeats
	pulls       int
	acked       []string
	deadletter  []remotequeue.Task
	failAck     bool // make /ack return 500
	failPull    bool // make /pull return 500
	dlReason    string
}

func (w *fakeWorker) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()

	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(rw http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+drainToken {
				rw.WriteHeader(http.StatusUnauthorized)
				return
			}
			next(rw, r)
		}
	}

	mux.HandleFunc("/pull", auth(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.failPull {
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		var batch []remotequeue.Task
		if len(w.pullBatches) > 0 {
			idx := w.pulls
			if idx >= len(w.pullBatches) {
				idx = len(w.pullBatches) - 1
			}
			batch = w.pullBatches[idx]
		}
		w.pulls++
		_ = json.NewEncoder(rw).Encode(map[string]any{"tasks": batch})
	}))

	mux.HandleFunc("/ack", auth(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.failAck {
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		var body struct {
			IDs []string `json:"ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.acked = append(w.acked, body.IDs...)
		_ = json.NewEncoder(rw).Encode(map[string]any{"deleted": len(body.IDs)})
	}))

	mux.HandleFunc("/deadletter", auth(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(rw).Encode(map[string]any{"tasks": w.deadletter})
			return
		}
		var body struct {
			IDs    []string `json:"ids"`
			Reason string   `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.dlReason = body.Reason
		for _, id := range body.IDs {
			w.deadletter = append(w.deadletter, remotequeue.Task{ID: id, Reason: body.Reason})
		}
		rw.WriteHeader(http.StatusOK)
	}))

	return mux
}

// newDrain wires a DrainService against the fake worker, writing into a temp
// vault. isClient marks "acme" as the only configured client.
func newDrain(t *testing.T, w *fakeWorker) (service.DrainService, string) {
	t.Helper()
	ts := httptest.NewServer(w.handler(t))
	t.Cleanup(ts.Close)

	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "10-tasks")
	taskFile := filepath.Join(tasksDir, "inbox.md")

	client := remotequeue.NewClient(ts.URL, drainToken)
	drain := service.DrainService{
		Tasks:    service.TaskService{TaskFilePath: taskFile, TasksDir: tasksDir},
		Queue:    queueAdapter{c: client},
		IsClient: func(name string) bool { return name == "acme" },
	}
	return drain, tasksDir
}

func countLines(t *testing.T, tasksDir, file string) []vaultTask {
	t.Helper()
	tasks, err := vault.ReadTasks(filepath.Join(tasksDir, file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	out := make([]vaultTask, 0, len(tasks))
	for _, tk := range tasks {
		out = append(out, vaultTask{ID: tk.ID, Text: tk.Text})
	}
	return out
}

type vaultTask struct {
	ID   string
	Text string
}

func TestDrain_HappyPath(t *testing.T) {
	w := &fakeWorker{pullBatches: [][]remotequeue.Task{{
		{ID: "qi-11111111", Text: "Buy milk", Project: "home"},
	}}}
	drain, tasksDir := newDrain(t, w)

	summary, err := drain.Drain(context.Background(), 100)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if summary.Drained != 1 || summary.Rejected != 0 {
		t.Fatalf("summary = %+v", summary)
	}

	lines := countLines(t, tasksDir, "home.md")
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d: %+v", len(lines), lines)
	}
	if lines[0].ID != "qi-11111111" {
		t.Fatalf("cloud id not used: %+v", lines[0])
	}
	if len(w.acked) != 1 || w.acked[0] != "qi-11111111" {
		t.Fatalf("acked = %v", w.acked)
	}
}

func TestDrain_IdempotentReDrain(t *testing.T) {
	task := remotequeue.Task{ID: "qi-22222222", Text: "Same task", Project: "home"}
	w := &fakeWorker{pullBatches: [][]remotequeue.Task{{task}}} // last batch repeats
	drain, tasksDir := newDrain(t, w)

	for i := 0; i < 2; i++ {
		if _, err := drain.Drain(context.Background(), 100); err != nil {
			t.Fatalf("drain %d: %v", i, err)
		}
	}

	lines := countLines(t, tasksDir, "home.md")
	if len(lines) != 1 {
		t.Fatalf("re-drain duplicated task: want 1 line, got %d", len(lines))
	}
}

func TestDrain_AckFailThenReDrain(t *testing.T) {
	task := remotequeue.Task{ID: "qi-33333333", Text: "Survive ack drop", Project: "home"}
	w := &fakeWorker{
		pullBatches: [][]remotequeue.Task{{task}},
		failAck:     true,
	}
	drain, tasksDir := newDrain(t, w)

	// First pass: write succeeds, ack fails → error, but the line is written.
	if _, err := drain.Drain(context.Background(), 100); err == nil {
		t.Fatal("expected ack failure to surface as error")
	}
	if got := countLines(t, tasksDir, "home.md"); len(got) != 1 {
		t.Fatalf("after ack-fail want 1 line, got %d", len(got))
	}

	// Ack recovers; re-drain re-pulls the same row → idempotency skips re-write.
	w.mu.Lock()
	w.failAck = false
	w.mu.Unlock()
	if _, err := drain.Drain(context.Background(), 100); err != nil {
		t.Fatalf("second drain: %v", err)
	}

	if got := countLines(t, tasksDir, "home.md"); len(got) != 1 {
		t.Fatalf("ack-fail + re-drain duplicated: want 1 line, got %d", len(got))
	}
}

func TestDrain_ValidationFailureDeadletters(t *testing.T) {
	w := &fakeWorker{pullBatches: [][]remotequeue.Task{{
		{ID: "qi-44444444", Text: "Bad client", Client: "ghost"},
		{ID: "qi-55555555", Text: "Has\nnewline"},
	}}}
	drain, tasksDir := newDrain(t, w)

	summary, err := drain.Drain(context.Background(), 100)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if summary.Drained != 0 || summary.Rejected != 2 {
		t.Fatalf("summary = %+v", summary)
	}

	// Nothing written anywhere.
	if got := countLines(t, tasksDir, "inbox.md"); len(got) != 0 {
		t.Fatalf("inbox should be empty, got %d", len(got))
	}
	// Both deadlettered, none acked.
	if len(w.deadletter) != 2 {
		t.Fatalf("want 2 deadlettered, got %d", len(w.deadletter))
	}
	if len(w.acked) != 0 {
		t.Fatalf("nothing should be acked, got %v", w.acked)
	}
}

func TestDrain_PullNetworkFailure(t *testing.T) {
	w := &fakeWorker{failPull: true}
	drain, tasksDir := newDrain(t, w)

	if _, err := drain.Drain(context.Background(), 100); err == nil {
		t.Fatal("expected pull failure to return an error")
	}
	// No partial write.
	if got := countLines(t, tasksDir, "inbox.md"); len(got) != 0 {
		t.Fatalf("pull failure must not write, got %d", len(got))
	}
}

func TestDrain_IDCollisionReMints(t *testing.T) {
	// First drain writes qi-66666666=Original. Second drain pulls a DIFFERENT
	// task under the SAME id → drainer re-mints, both survive.
	w := &fakeWorker{pullBatches: [][]remotequeue.Task{
		{{ID: "qi-66666666", Text: "Original", Project: "home"}},
		{{ID: "qi-66666666", Text: "Different", Project: "home"}},
	}}
	drain, tasksDir := newDrain(t, w)

	if _, err := drain.Drain(context.Background(), 100); err != nil {
		t.Fatalf("first drain: %v", err)
	}
	if _, err := drain.Drain(context.Background(), 100); err != nil {
		t.Fatalf("second drain: %v", err)
	}

	lines := countLines(t, tasksDir, "home.md")
	if len(lines) != 2 {
		t.Fatalf("id collision: want 2 lines, got %d: %+v", len(lines), lines)
	}
	ids := map[string]bool{}
	for _, l := range lines {
		ids[l.ID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("want 2 distinct ids, got %v", ids)
	}
}
