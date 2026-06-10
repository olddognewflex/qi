package daemon

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"

	"qi/internal/policy"
	"qi/internal/tools"
	"qi/internal/tools/builtin"
)

// HTTPHandler builds the HTTP surface for off-machine task creation. Every
// request must carry "Authorization: Bearer <token>"; the body is forwarded to
// the task.add tool as the CallerRemote identity, so the policy allowlist —
// not the transport — decides what a remote caller may do. token must be
// non-empty; callers gate on RemoteConfig before invoking this.
//
// Routes:
//
//	POST /task     {text, project?, client?, due?, schedule?} -> {id, path, project}
//	GET  /healthz  (no auth) -> 200 "ok"
func (s *Server) HTTPHandler(token string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.Handle("POST /task", s.authed(token, s.handleTaskAdd))

	return mux
}

// authed wraps next with a constant-time bearer-token check.
func (s *Server) authed(token string, next http.HandlerFunc) http.Handler {
	want := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			httpError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	})
}

func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	// Read and re-marshal the body so we forward exactly the task.add schema
	// fields and reject anything malformed before it reaches the tool.
	var body struct {
		Text     string `json:"text"`
		Project  string `json:"project,omitempty"`
		Client   string `json:"client,omitempty"`
		Due      string `json:"due,omitempty"`
		Schedule string `json:"schedule,omitempty"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	args, err := json.Marshal(body)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result, err := s.CallTool(r.Context(), policy.CallerRemote, builtin.TaskAddToolName, args)
	if err != nil {
		s.writeCallError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(result)
}

// writeCallError maps a CallTool error to an HTTP status.
func (s *Server) writeCallError(w http.ResponseWriter, err error) {
	var pde *policyDeniedError
	var ipe *invalidParamsError
	switch {
	case errors.As(err, &pde):
		httpError(w, http.StatusForbidden, pde.Error())
	case errors.Is(err, tools.ErrNotFound):
		httpError(w, http.StatusNotFound, err.Error())
	case errors.As(err, &ipe):
		httpError(w, http.StatusBadRequest, err.Error())
	default:
		httpError(w, http.StatusBadRequest, err.Error())
	}
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: msg})
}
