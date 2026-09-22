package typesafe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient points a Client at srv with a negligible backoff.
func newTestClient(srv *httptest.Server) *Client {
	c := NewClient(srv.URL, "sk-test", "", srv.Client())
	c.backoff = time.Millisecond
	return c
}

const choiceResponse = `{
  "model": "jev-1.13.0",
  "answers": {"action": {"type": "choice", "choice": "task",
    "probabilities": {"task": 0.9, "note": 0.08, "archive": 0.02}, "confidence": 0.81}},
  "usage": {"input_tokens": 318, "output_tokens": 34}
}`

func TestNewClientDefaults(t *testing.T) {
	c := NewClient("", "k", "", nil)
	if c.baseURL != DefaultURL || c.model != DefaultModel || c.http != http.DefaultClient {
		t.Errorf("defaults not applied: %+v", c)
	}
}

func TestSystemOneRequestShapeAndDecode(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
			t.Errorf("request = %s %s, want POST /v1/systemone", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("Authorization = %q", auth)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		io.WriteString(w, choiceResponse)
	}))
	defer srv.Close()

	resp, err := newTestClient(srv).SystemOne(context.Background(), Request{
		State:     "hello",
		Questions: map[string]Question{"q": {Type: TypeNoul, Instructions: "Is it?"}},
	})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if got["model"] != DefaultModel || got["state"] != "hello" {
		t.Errorf("body = %v", got)
	}
	q := got["questions"].(map[string]any)["q"].(map[string]any)
	if _, has := q["criteria"]; has {
		t.Errorf("criteria should be omitted when nil: %v", q)
	}
	if q["type"] != "noul" || q["instructions"] != "Is it?" {
		t.Errorf("question = %v", q)
	}

	ans := resp.Answers["action"]
	if resp.Model != "jev-1.13.0" || ans.Choice != "task" || ans.Confidence != 0.81 || ans.Probabilities["note"] != 0.08 {
		t.Errorf("decoded = %+v", resp)
	}
	if resp.Usage.InputTokens != 318 || resp.Usage.OutputTokens != 34 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestSystemOneStatusHandling(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []int // status per attempt; 200 serves choiceResponse
		wantCalls int32
		wantErr   int // 0 = success, else expected APIError.Status
	}{
		{"401 not retried", []int{401}, 1, 401},
		{"422 not retried", []int{422}, 1, 422},
		{"429 then 200 retries", []int{429, 200}, 2, 0},
		{"529 twice then 200", []int{529, 529, 200}, 3, 0},
		{"429 exhausts attempts", []int{429, 429, 429, 200}, 3, 429},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				status := tc.statuses[n-1]
				if status != 200 {
					w.WriteHeader(status)
					io.WriteString(w, `{"error":"nope"}`)
					return
				}
				io.WriteString(w, choiceResponse)
			}))
			defer srv.Close()

			_, err := newTestClient(srv).SystemOne(context.Background(), Request{State: "x"})
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("calls = %d, want %d", got, tc.wantCalls)
			}
			if tc.wantErr == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.wantErr {
				t.Fatalf("err = %v, want APIError %d", err, tc.wantErr)
			}
			if !strings.Contains(apiErr.Body, "nope") {
				t.Errorf("APIError.Body = %q", apiErr.Body)
			}
		})
	}
}

func TestSystemOneTruncatesErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		io.WriteString(w, strings.Repeat("x", 3*errBodyLimit))
	}))
	defer srv.Close()

	_, err := newTestClient(srv).SystemOne(context.Background(), Request{State: "x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || len(apiErr.Body) != errBodyLimit {
		t.Fatalf("err = %v (body len %d), want truncated to %d", err, len(apiErr.Body), errBodyLimit)
	}
}

func TestSystemOneHonoursContextDuringBackoff(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.backoff = time.Hour // a cancel must cut the wait short
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.SystemOne(ctx, Request{State: "x"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second || calls.Load() != 1 {
		t.Errorf("backoff not interrupted: calls=%d elapsed=%s", calls.Load(), time.Since(start))
	}
}

func TestInboxClassifierBatchRequestShape(t *testing.T) {
	var got struct {
		State struct {
			Context  string              `json:"context"`
			Captures []map[string]string `json:"captures"`
		} `json:"state"`
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		io.WriteString(w, `{"model":"jev","answers":{
			"action_0":{"type":"choice","choice":"task","probabilities":{"task":0.9},"confidence":0.81},
			"action_1":{"type":"choice","choice":"note","confidence":0.6},
			"action_2":{"type":"choice","choice":"archive","confidence":0.7}}}`)
	}))
	defer srv.Close()

	bodies := [][]string{{"buy milk", "and eggs"}, {"idea: cache the index"}, {"asdf"}}
	res, err := InboxClassifier{Client: newTestClient(srv)}.ClassifyInbox(context.Background(), bodies)
	if err != nil {
		t.Fatalf("ClassifyInbox: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("requests = %d, want 1 for the whole batch", calls.Load())
	}

	if got.State.Context != inboxContext || len(got.State.Captures) != 3 {
		t.Fatalf("state = %+v", got.State)
	}
	if got.State.Captures[0]["text"] != "buy milk\nand eggs" || got.State.Captures[2]["text"] != "asdf" {
		t.Errorf("captures = %v", got.State.Captures)
	}
	if len(got.Questions) != 3 {
		t.Fatalf("questions = %d, want 3", len(got.Questions))
	}
	for i := range bodies {
		id := fmt.Sprintf("action_%d", i)
		q, ok := got.Questions[id]
		if !ok || q.Type != TypeChoice {
			t.Fatalf("question %s = %+v", id, q)
		}
		instr, _ := q.Instructions.(map[string]any)
		wantPath := fmt.Sprintf("`captures[%d].text`", i)
		if s, _ := instr["question"].(string); !strings.Contains(s, wantPath) {
			t.Errorf("%s question %q does not reference %s", id, s, wantPath)
		}
		if s, _ := instr["focus"].(string); !strings.Contains(s, "Judge only this capture") {
			t.Errorf("%s focus = %q", id, s)
		}
		crit, _ := q.Criteria.(map[string]any)
		if len(crit) != 3 {
			t.Errorf("%s criteria has %d options, want 3", id, len(crit))
		}
		for _, opt := range []string{InboxTask, InboxNote, InboxArchive} {
			if s, _ := crit[opt].(string); s == "" {
				t.Errorf("%s criteria missing %q: %v", id, opt, crit)
			}
		}
	}

	want := []InboxClassification{
		{Action: InboxTask, Confidence: 0.81, Probabilities: map[string]float64{"task": 0.9}},
		{Action: InboxNote, Confidence: 0.6},
		{Action: InboxArchive, Confidence: 0.7},
	}
	if len(res) != len(want) {
		t.Fatalf("results = %d, want %d", len(res), len(want))
	}
	for i, w := range want {
		if res[i].Err != nil || res[i].Action != w.Action || res[i].Confidence != w.Confidence {
			t.Errorf("result %d = %+v, want %+v", i, res[i], w)
		}
	}
	if res[0].Probabilities["task"] != 0.9 {
		t.Errorf("probabilities not mapped: %v", res[0].Probabilities)
	}
}

func TestInboxClassifierMissingAnswerIsPerItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// action_1 absent, action_2 present but empty.
		io.WriteString(w, `{"model":"jev","answers":{
			"action_0":{"type":"choice","choice":"note","confidence":0.9},
			"action_2":{"type":"choice"}}}`)
	}))
	defer srv.Close()

	res, err := InboxClassifier{Client: newTestClient(srv)}.ClassifyInbox(context.Background(), [][]string{{"a"}, {"b"}, {"c"}})
	if err != nil {
		t.Fatalf("a missing answer must not fail the batch: %v", err)
	}
	if res[0].Err != nil || res[0].Action != InboxNote {
		t.Errorf("result 0 = %+v", res[0])
	}
	for _, i := range []int{1, 2} {
		if res[i].Err == nil || !strings.Contains(res[i].Err.Error(), fmt.Sprintf("action_%d", i)) {
			t.Errorf("result %d err = %v, want missing action_%d", i, res[i].Err, i)
		}
	}
}

func TestInboxClassifierBatchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
	}))
	defer srv.Close()

	res, err := InboxClassifier{Client: newTestClient(srv)}.ClassifyInbox(context.Background(), [][]string{{"a"}, {"b"}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || res != nil {
		t.Fatalf("res, err = %v, %v; want nil, APIError", res, err)
	}
}

func TestInboxClassifierEmptyBatchSendsNothing(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()

	res, err := InboxClassifier{Client: newTestClient(srv)}.ClassifyInbox(context.Background(), nil)
	if res != nil || err != nil || calls.Load() != 0 {
		t.Errorf("empty batch: res=%v err=%v calls=%d", res, err, calls.Load())
	}
}
