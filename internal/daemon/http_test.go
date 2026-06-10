package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"qi/internal/policy"
	"qi/internal/service"
	"qi/internal/tools"
	"qi/internal/tools/builtin"
)

const testToken = "s3cret-token"

func newHTTPServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	svc := service.TaskService{
		TaskFilePath: filepath.Join(dir, "inbox.md"),
		TasksDir:     dir,
	}
	r := tools.NewRegistry()
	if err := builtin.RegisterTaskAdd(r, svc, func(string) bool { return true }); err != nil {
		t.Fatalf("register task.add: %v", err)
	}
	decider := policy.NewRemoteDecider(builtin.TaskAddToolName)
	srv := NewServer(r, decider, nil, nil)
	ts := httptest.NewServer(srv.HTTPHandler(testToken))
	t.Cleanup(ts.Close)
	return ts, dir
}

func postTask(t *testing.T, base, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/task", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHTTPTaskAddRequiresToken(t *testing.T) {
	ts, _ := newHTTPServer(t)
	resp := postTask(t, ts.URL, "", `{"text":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestHTTPTaskAddWrongToken(t *testing.T) {
	ts, _ := newHTTPServer(t)
	resp := postTask(t, ts.URL, "nope", `{"text":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestHTTPTaskAddCreates(t *testing.T) {
	ts, dir := newHTTPServer(t)
	resp := postTask(t, ts.URL, testToken, `{"text":"Buy milk","project":"home"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var res struct {
		ID, Path, Project string
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(res.ID, "qi-") {
		t.Fatalf("id %q not minted", res.ID)
	}
	if res.Path != filepath.Join(dir, "home.md") {
		t.Fatalf("path %q not routed to project file", res.Path)
	}
}

func TestHTTPTaskAddBadJSON(t *testing.T) {
	ts, _ := newHTTPServer(t)
	resp := postTask(t, ts.URL, testToken, `{"text":"x","bogus":1}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTPHealthz(t *testing.T) {
	ts, _ := newHTTPServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
