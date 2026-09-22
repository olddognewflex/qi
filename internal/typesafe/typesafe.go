// Package typesafe is a thin client for the TypeSafe System One evaluation API
// (POST /v1/systemone). It depends only on the stdlib, like internal/embed.
// It is used opt-in by `qi inbox` triage; it is never on the capture hot path
// and never wired into qid's deterministic skills.
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultURL is the TypeSafe API base URL used when none is configured.
const DefaultURL = "https://api.typesafe.ai"

// DefaultModel is the System One model used when none is configured.
const DefaultModel = "jev-latest"

// maxAttempts bounds how many times a 429/529 response is retried in total
// (the first try included).
const maxAttempts = 3

// errBodyLimit caps how much of a non-2xx response body an APIError keeps.
const errBodyLimit = 4096

// Question types.
const (
	TypeChoice = "choice"
	TypeNoul   = "noul"
	TypeScore  = "score"
)

// Request is one System One evaluation: a State and the typed Questions to ask
// about it. Model is filled in by the Client.
type Request struct {
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Question is one typed question. Instructions and Criteria may be a string,
// object, or array (see the API reference); Criteria is required for choice
// and score questions and optional for noul.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer is the answer to one Question. Which fields are set depends on Type:
// choice sets Choice/Probabilities/Confidence, noul sets Noul, score sets
// Score/Probabilities/Confidence.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         float64            `json:"score,omitempty"`
}

// Usage is the token accounting for one request.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response carries one Answer per question id.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// APIError is a non-2xx response. Body is the (truncated) response body, which
// TypeSafe fills with a JSON description of the failure.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("typesafe: %d %s: %s", e.Status, http.StatusText(e.Status), strings.TrimSpace(e.Body))
}

// retryable reports whether a status is worth retrying: rate limited (429) or
// overloaded (529). Everything else (401, 422, ...) fails immediately.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == 529
}

// Client calls the TypeSafe API.
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	// backoff is the first retry delay; it doubles per attempt. A field so
	// tests can shrink it.
	backoff time.Duration
}

// NewClient constructs a client. Pass "" for baseURL / model to use DefaultURL
// / DefaultModel, and nil for httpClient to use http.DefaultClient.
func NewClient(baseURL, apiKey, model string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	if model == "" {
		model = DefaultModel
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    httpClient,
		backoff: 500 * time.Millisecond,
	}
}

type wireRequest struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// SystemOne evaluates req. 429/529 responses are retried with exponential
// backoff (at most maxAttempts tries), honouring ctx between attempts; any
// other non-2xx response is returned as an *APIError without retrying.
func (c *Client) SystemOne(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(wireRequest{State: req.State, Model: c.model, Questions: req.Questions})
	if err != nil {
		return Response{}, fmt.Errorf("typesafe: marshal request: %w", err)
	}

	delay := c.backoff
	for attempt := 1; ; attempt++ {
		resp, err := c.do(ctx, body)
		var apiErr *APIError
		if err == nil || !errors.As(err, &apiErr) || !retryable(apiErr.Status) || attempt >= maxAttempts {
			return resp, err
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return Response{}, fmt.Errorf("typesafe: %w (last: %v)", ctx.Err(), err)
		case <-t.C:
		}
		delay *= 2
	}
}

// do performs one HTTP round trip.
func (c *Client) do(ctx context.Context, body []byte) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("typesafe: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("typesafe: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return Response{}, &APIError{Status: resp.StatusCode, Body: string(buf)}
	}

	var out Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Response{}, fmt.Errorf("typesafe: decode response: %w", err)
	}
	return out, nil
}
