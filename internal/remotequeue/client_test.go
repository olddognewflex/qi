package remotequeue

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPullSendsBearerAndLimit(t *testing.T) {
	var gotAuth, gotLimit string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotLimit = r.URL.Query().Get("limit")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tasks": []Task{{ID: "qi-aaaaaaaa", Text: "hello", Kind: "note", Project: "p"}},
		})
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	tasks, err := c.Pull(context.Background(), 50)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotLimit != "50" {
		t.Fatalf("limit = %q", gotLimit)
	}
	if len(tasks) != 1 || tasks[0].ID != "qi-aaaaaaaa" {
		t.Fatalf("tasks = %+v", tasks)
	}
	if tasks[0].Kind != "note" {
		t.Fatalf("kind did not round-trip: %q", tasks[0].Kind)
	}
}

func TestAckPostsIDs(t *testing.T) {
	var body struct {
		IDs []string `json:"ids"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	if err := c.Ack(context.Background(), []string{"qi-1", "qi-2"}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if len(body.IDs) != 2 {
		t.Fatalf("ids = %v", body.IDs)
	}
}

func TestAckEmptyIsNoOp(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	if err := c.Ack(context.Background(), nil); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if called {
		t.Fatal("empty ack should not hit the network")
	}
}

func TestDeadletterPostsReason(t *testing.T) {
	var body struct {
		IDs    []string `json:"ids"`
		Reason string   `json:"reason"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	if err := c.Deadletter(context.Background(), []string{"qi-x"}, "bad client"); err != nil {
		t.Fatalf("deadletter: %v", err)
	}
	if body.Reason != "bad client" || len(body.IDs) != 1 {
		t.Fatalf("body = %+v", body)
	}
}

func TestNon2xxReturnsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "nope")
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	if _, err := c.Pull(context.Background(), 10); err == nil {
		t.Fatal("expected error on 401")
	}
}
