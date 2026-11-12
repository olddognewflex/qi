package typesafe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckDuplicatesRequestAndAnswers(t *testing.T) {
	var body map[string]any
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		// dup_0_1 is missing: that pair reads as -1.
		io.WriteString(w, `{"answers": {
			"dup_0_0": {"type": "noul", "noul": 0.91},
			"dup_1_0": {"type": "noul", "noul": 0.12}
		}, "usage": {"input_tokens": 40, "output_tokens": 3}}`)
	}))
	defer srv.Close()

	c := InboxClassifier{Client: newTestClient(srv)}
	got, usage, err := c.CheckDuplicates(context.Background(), []DupCheck{
		{Body: []string{"renew my passport"}, Candidates: []string{"renew passport", "passport photos"}},
		{Body: []string{"buy milk"}, Candidates: []string{"buy oat milk"}},
		{Body: []string{"no candidates"}},
	})
	if err != nil {
		t.Fatalf("CheckDuplicates: %v", err)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want one per batch", requests)
	}
	if len(got) != 3 ||
		len(got[0].Probabilities) != 2 || got[0].Probabilities[0] != 0.91 || got[0].Probabilities[1] != -1 ||
		len(got[1].Probabilities) != 1 || got[1].Probabilities[0] != 0.12 ||
		len(got[2].Probabilities) != 0 {
		t.Errorf("results = %+v", got)
	}
	if usage.InputTokens != 40 {
		t.Errorf("usage = %+v", usage)
	}
	qs := body["questions"].(map[string]any)
	if len(qs) != 3 {
		t.Errorf("questions = %d, want one Noul per (capture, candidate) pair", len(qs))
	}
	q := qs["dup_0_1"].(map[string]any)
	if q["type"] != TypeNoul || !strings.Contains(q["instructions"].(map[string]any)["question"].(string), "captures[0].open_tasks[1]") {
		t.Errorf("dup_0_1 = %v", q)
	}
	caps := body["state"].(map[string]any)["captures"].([]any)
	if first := caps[0].(map[string]any); first["text"] != "renew my passport" {
		t.Errorf("capture 0 = %v", first)
	}
}

func TestCheckDuplicatesNoCandidatesNoRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request sent with no candidates")
	}))
	defer srv.Close()
	got, _, err := InboxClassifier{Client: newTestClient(srv)}.CheckDuplicates(context.Background(), []DupCheck{{Body: []string{"x"}}})
	if err != nil || len(got) != 1 {
		t.Errorf("got %+v, %v", got, err)
	}
}
