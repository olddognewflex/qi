package index

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"qi/internal/domain"
)

func TestEncodeDecodeVectorRoundTrip(t *testing.T) {
	in := []float32{0, 1, -1, 0.5, 3.14159, -2.71828}
	out := decodeVector(encodeVector(in))
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("out[%d] = %v, want %v", i, out[i], in[i])
		}
	}
}

func TestCosine(t *testing.T) {
	if got := cosine([]float32{1, 0}, []float32{1, 0}); math.Abs(got-1) > 1e-6 {
		t.Errorf("identical = %v, want 1", got)
	}
	if got := cosine([]float32{1, 0}, []float32{0, 1}); math.Abs(got) > 1e-6 {
		t.Errorf("orthogonal = %v, want 0", got)
	}
	if got := cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("zero-norm = %v, want 0", got)
	}
}

func TestUpsertAndEmbeddingsFor(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	if err := idx.UpsertEmbedding("/v/a.md", "m", []float32{1, 2, 3}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Upsert again (conflict path) with a new vector.
	if err := idx.UpsertEmbedding("/v/a.md", "m", []float32{4, 5, 6}); err != nil {
		t.Fatalf("upsert2: %v", err)
	}

	got, err := idx.EmbeddingsFor("m")
	if err != nil {
		t.Fatalf("embeddings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("count = %d, want 1", len(got))
	}
	if got[0].Path != "/v/a.md" {
		t.Errorf("path = %q", got[0].Path)
	}
	want := []float32{4, 5, 6}
	for i := range want {
		if got[0].Vector[i] != want[i] {
			t.Errorf("vector[%d] = %v, want %v", i, got[0].Vector[i], want[i])
		}
	}

	// Other models are not returned.
	if other, _ := idx.EmbeddingsFor("nope"); len(other) != 0 {
		t.Errorf("other model count = %d, want 0", len(other))
	}

	if err := idx.ClearEmbeddings(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if after, _ := idx.EmbeddingsFor("m"); len(after) != 0 {
		t.Errorf("after clear = %d, want 0", len(after))
	}
}

func TestSemanticSearchRanksByCosine(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	vault := t.TempDir()
	notesDir := filepath.Join(vault, "20-notes")
	os.MkdirAll(notesDir, 0o755)
	os.WriteFile(filepath.Join(notesDir, "x.md"), []byte("# X\n\nbody of x note here\n"), 0o644)
	os.WriteFile(filepath.Join(notesDir, "y.md"), []byte("# Y\n\nbody of y note here\n"), 0o644)
	dailyDir := filepath.Join(vault, "30-daily")
	os.MkdirAll(dailyDir, 0o755)
	os.WriteFile(filepath.Join(dailyDir, "z.md"), []byte("# Z\n\nbody of z daily here\n"), 0o644)

	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Seed the FTS table so previews resolve.
	if err := idx.Rebuild(vault); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	px := filepath.Join(notesDir, "x.md")
	py := filepath.Join(notesDir, "y.md")
	pz := filepath.Join(dailyDir, "z.md")
	// Three orthogonal-ish directions.
	idx.UpsertEmbedding(px, "m", []float32{1, 0, 0})
	idx.UpsertEmbedding(py, "m", []float32{0, 1, 0})
	idx.UpsertEmbedding(pz, "m", []float32{0, 0, 1})

	// Query close to x.
	res, err := idx.SemanticSearch("m", []float32{0.9, 0.1, 0}, SearchOptions{})
	if err != nil {
		t.Fatalf("semantic: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("count = %d, want 3", len(res))
	}
	if res[0].Note.Path != px {
		t.Errorf("top = %q, want %q", res[0].Note.Path, px)
	}
	if res[0].Rank <= res[1].Rank {
		t.Errorf("not sorted desc: %v <= %v", res[0].Rank, res[1].Rank)
	}
	if res[0].Note.Title != "X" {
		t.Errorf("title = %q, want X (preview lookup failed)", res[0].Note.Title)
	}
	if res[0].Match == "" {
		t.Error("expected non-empty preview")
	}

	// Kind filter: only the daily.
	filtered, err := idx.SemanticSearch("m", []float32{0, 0, 1}, SearchOptions{Kinds: []string{domain.SearchKindDaily}})
	if err != nil {
		t.Fatalf("semantic filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Note.Path != pz {
		t.Fatalf("filtered = %+v, want only %q", filtered, pz)
	}
	if filtered[0].Kind != domain.SearchKindDaily {
		t.Errorf("kind = %q, want daily", filtered[0].Kind)
	}
}

func TestSemanticSearchEmptyStore(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	idx, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	res, err := idx.SemanticSearch("m", []float32{1, 0}, SearchOptions{})
	if err != nil {
		t.Fatalf("semantic: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("count = %d, want 0", len(res))
	}
}
