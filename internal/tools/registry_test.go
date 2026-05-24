package tools

import (
	"sync"
	"testing"
)

func localTool(name string) Tool {
	return Tool{Name: name, Source: Source{Kind: SourceLocal}, Description: name}
}

func mcpTool(server, name string) Tool {
	return Tool{Name: name, Source: Source{Kind: SourceMCP, ID: server}}
}

func TestRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(localTool("vault.capture")); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, ok := r.Lookup("vault.capture")
	if !ok {
		t.Fatal("lookup miss after register")
	}
	if got.Name != "vault.capture" {
		t.Fatalf("name = %q", got.Name)
	}
}

func TestRegisterDuplicateRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(localTool("a")); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register(localTool("a")); err == nil {
		t.Fatal("expected duplicate register to error")
	}
	if r.Len() != 1 {
		t.Fatalf("len = %d, want 1", r.Len())
	}
}

func TestRegisterValidation(t *testing.T) {
	r := NewRegistry()
	cases := []struct {
		name string
		tool Tool
	}{
		{"empty name", Tool{Source: Source{Kind: SourceLocal}}},
		{"whitespace name", Tool{Name: "bad name", Source: Source{Kind: SourceLocal}}},
		{"mcp without id", Tool{Name: "x", Source: Source{Kind: SourceMCP}}},
		{"local with id", Tool{Name: "x", Source: Source{Kind: SourceLocal, ID: "nope"}}},
		{"unknown kind", Tool{Name: "x", Source: Source{Kind: 99}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := r.Register(c.tool); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRegisterDynamicReplacesSameSource(t *testing.T) {
	r := NewRegistry()
	src := Source{Kind: SourceMCP, ID: "github"}
	first := []Tool{mcpTool("github", "create_issue"), mcpTool("github", "list_repos")}
	if err := r.RegisterDynamic(src, first); err != nil {
		t.Fatalf("first batch: %v", err)
	}

	second := []Tool{mcpTool("github", "create_pr")}
	if err := r.RegisterDynamic(src, second); err != nil {
		t.Fatalf("second batch: %v", err)
	}

	if r.Len() != 1 {
		t.Fatalf("len = %d, want 1 after replacement", r.Len())
	}
	if _, ok := r.Lookup("create_issue"); ok {
		t.Fatal("create_issue should have been removed")
	}
	if _, ok := r.Lookup("create_pr"); !ok {
		t.Fatal("create_pr should be present")
	}
}

func TestRegisterDynamicLeavesOtherSourcesAlone(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(localTool("vault.capture")); err != nil {
		t.Fatalf("local register: %v", err)
	}
	src := Source{Kind: SourceMCP, ID: "github"}
	if err := r.RegisterDynamic(src, []Tool{mcpTool("github", "create_issue")}); err != nil {
		t.Fatalf("mcp register: %v", err)
	}
	if err := r.RegisterDynamic(src, []Tool{mcpTool("github", "create_pr")}); err != nil {
		t.Fatalf("mcp replace: %v", err)
	}
	if _, ok := r.Lookup("vault.capture"); !ok {
		t.Fatal("local tool should survive dynamic replace")
	}
}

func TestRegisterDynamicRejectsCrossSourceCollision(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(localTool("create_issue")); err != nil {
		t.Fatalf("local register: %v", err)
	}
	src := Source{Kind: SourceMCP, ID: "github"}
	err := r.RegisterDynamic(src, []Tool{mcpTool("github", "create_issue")})
	if err == nil {
		t.Fatal("expected collision error")
	}
	if _, ok := r.Lookup("create_issue"); !ok {
		t.Fatal("local tool should still be present after rejected batch")
	}
}

func TestRegisterDynamicAtomicOnInvalidEntry(t *testing.T) {
	r := NewRegistry()
	src := Source{Kind: SourceMCP, ID: "github"}
	if err := r.RegisterDynamic(src, []Tool{mcpTool("github", "ok")}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	batch := []Tool{mcpTool("github", "fine"), {Name: "", Source: src}}
	if err := r.RegisterDynamic(src, batch); err == nil {
		t.Fatal("expected validation error")
	}
	if _, ok := r.Lookup("ok"); !ok {
		t.Fatal("prior tool removed despite failed batch")
	}
	if _, ok := r.Lookup("fine"); ok {
		t.Fatal("partial batch leaked into registry")
	}
}

func TestRegisterDynamicRejectsMismatchedSource(t *testing.T) {
	r := NewRegistry()
	src := Source{Kind: SourceMCP, ID: "github"}
	batch := []Tool{mcpTool("gitlab", "create_issue")}
	if err := r.RegisterDynamic(src, batch); err == nil {
		t.Fatal("expected source-mismatch error")
	}
}

func TestUnregisterBySource(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(localTool("vault.capture")); err != nil {
		t.Fatalf("local: %v", err)
	}
	src := Source{Kind: SourceMCP, ID: "github"}
	if err := r.RegisterDynamic(src, []Tool{mcpTool("github", "a"), mcpTool("github", "b")}); err != nil {
		t.Fatalf("mcp: %v", err)
	}

	n := r.Unregister(src)
	if n != 2 {
		t.Fatalf("removed = %d, want 2", n)
	}
	if r.Len() != 1 {
		t.Fatalf("len = %d, want 1", r.Len())
	}
	if _, ok := r.Lookup("vault.capture"); !ok {
		t.Fatal("local tool should remain")
	}
}

func TestUnregisterNoMatch(t *testing.T) {
	r := NewRegistry()
	if got := r.Unregister(Source{Kind: SourceMCP, ID: "ghost"}); got != 0 {
		t.Fatalf("removed = %d, want 0", got)
	}
}

func TestListSorted(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"c", "a", "b"} {
		if err := r.Register(localTool(name)); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	got := r.List()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, t2 := range got {
		if t2.Name != want[i] {
			t.Errorf("[%d] = %q, want %q", i, t2.Name, want[i])
		}
	}
}

func TestLookupMiss(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Lookup("nope"); ok {
		t.Fatal("expected miss")
	}
}

func TestConcurrentRegisterAndList(t *testing.T) {
	r := NewRegistry()
	const writers, readers = 16, 16
	var wg sync.WaitGroup
	wg.Add(writers + readers)

	for i := 0; i < writers; i++ {
		go func(id int) {
			defer wg.Done()
			src := Source{Kind: SourceMCP, ID: name(id)}
			batch := []Tool{
				{Name: name(id) + ".a", Source: src},
				{Name: name(id) + ".b", Source: src},
			}
			_ = r.RegisterDynamic(src, batch)
		}(i)
	}
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			_ = r.List()
		}()
	}
	wg.Wait()

	if r.Len() != writers*2 {
		t.Fatalf("len = %d, want %d", r.Len(), writers*2)
	}
}

func name(i int) string {
	const digits = "0123456789abcdef"
	if i < 16 {
		return string(digits[i])
	}
	return string(digits[i/16]) + string(digits[i%16])
}
