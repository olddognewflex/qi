// Package remotequeue is the laptop-side HTTP client for the cloud queue
// (the Cloudflare Worker described in docs/cloud-queue-spec.md §1). It is pure
// transport: pull pending tasks, ack the ones written to the vault, and
// deadletter the ones that fail validation. All routes carry the DRAIN bearer
// token. The client never writes the vault — `qi remote-drain` (service layer)
// does that — and holds no state beyond its base URL and token.
package remotequeue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Task is one queued task as returned by the Worker. Field names match the D1
// queue schema / Worker JSON contract in the spec. project/client/due/scheduled
// are optional (empty when the column was NULL).
type Task struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Kind      string `json:"kind,omitempty"` // "task" (default) | "note" | "capture"
	Project   string `json:"project,omitempty"`
	Client    string `json:"client,omitempty"`
	Due       string `json:"due,omitempty"`
	Scheduled string `json:"scheduled,omitempty"`
	Source    string `json:"source,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	Status    string `json:"status,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Client talks to one Worker deployment using the DRAIN token.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient returns a queue client for baseURL authenticating with the DRAIN
// token. A trailing slash on baseURL is trimmed so path joins are clean.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Pull fetches up to limit oldest pending tasks. It does not delete them — the
// rows stay pending until Ack. The Worker responds with {"tasks":[...]}.
func (c *Client) Pull(ctx context.Context, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 100
	}
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	resp, err := c.do(ctx, http.MethodGet, "/pull?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var body struct {
		Tasks []Task `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("remotequeue: decode pull response: %w", err)
	}
	return body.Tasks, nil
}

// Ack deletes the given ids from the queue (delete-on-ack). A nil/empty slice
// is a no-op.
func (c *Client) Ack(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	payload := map[string]any{"ids": ids}
	resp, err := c.doJSON(ctx, http.MethodPost, "/ack", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp)
}

// Deadletter marks the given ids as failed, recording reason. A nil/empty slice
// is a no-op.
func (c *Client) Deadletter(ctx context.Context, ids []string, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	payload := map[string]any{"ids": ids, "reason": reason}
	resp, err := c.doJSON(ctx, http.MethodPost, "/deadletter", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp)
}

// ListDeadletter returns the deadletter rows (for `qi remote-drain --show-failed`).
func (c *Client) ListDeadletter(ctx context.Context) ([]Task, error) {
	resp, err := c.do(ctx, http.MethodGet, "/deadletter", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var body struct {
		Tasks []Task `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("remotequeue: decode deadletter response: %w", err)
	}
	return body.Tasks, nil
}

// Stats are the queue depth counts returned by GET /stats.
type Stats struct {
	Pending    int `json:"pending"`
	Deadletter int `json:"deadletter"`
}

// Stats returns the pending and deadletter row counts from the Worker.
func (c *Client) Stats(ctx context.Context) (Stats, error) {
	resp, err := c.do(ctx, http.MethodGet, "/stats", nil)
	if err != nil {
		return Stats{}, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return Stats{}, err
	}
	var s Stats
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return Stats{}, fmt.Errorf("remotequeue: decode stats response: %w", err)
	}
	return s, nil
}

// doJSON marshals payload as a JSON body with the right content type.
func (c *Client) doJSON(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("remotequeue: encode request: %w", err)
	}
	return c.do(ctx, method, path, bytes.NewReader(b))
}

// do issues a request with the bearer token attached. A JSON body (if any) is
// flagged via Content-Type.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("remotequeue: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remotequeue: %s %s: %w", method, path, err)
	}
	return resp, nil
}

// checkStatus turns a non-2xx response into an error carrying the status and a
// snippet of the body for diagnosis.
func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("remotequeue: %s returned %d: %s", resp.Request.URL.Path, resp.StatusCode, strings.TrimSpace(string(snippet)))
}
