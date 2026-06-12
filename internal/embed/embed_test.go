package embed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedReturnsVector(t *testing.T) {
	var got embedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"nomic-embed-text","embeddings":[[0.1,0.2,0.3]]}`))
	}))
	defer srv.Close()

	e := NewOllamaEmbedder(srv.URL, "nomic-embed-text", nil)
	vec, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	want := []float32{0.1, 0.2, 0.3}
	if len(vec) != len(want) {
		t.Fatalf("len = %d, want %d", len(vec), len(want))
	}
	for i := range want {
		if vec[i] != want[i] {
			t.Errorf("vec[%d] = %v, want %v", i, vec[i], want[i])
		}
	}
	if got.Model != "nomic-embed-text" {
		t.Errorf("request model = %q", got.Model)
	}
	if got.Input != "hello world" {
		t.Errorf("request input = %q", got.Input)
	}
}

func TestEmbedDefaults(t *testing.T) {
	e := NewOllamaEmbedder("", "", nil)
	if e.Model() != DefaultModel {
		t.Errorf("Model() = %q, want %q", e.Model(), DefaultModel)
	}
	if e.baseURL != DefaultOllamaURL {
		t.Errorf("baseURL = %q, want %q", e.baseURL, DefaultOllamaURL)
	}
}

func TestEmbedNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()

	e := NewOllamaEmbedder(srv.URL, "nomic-embed-text", nil)
	_, err := e.Embed(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error on non-200")
	}
}

func TestEmbedEmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"nomic-embed-text","embeddings":[]}`))
	}))
	defer srv.Close()

	e := NewOllamaEmbedder(srv.URL, "nomic-embed-text", nil)
	_, err := e.Embed(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error on empty embeddings array")
	}
}
