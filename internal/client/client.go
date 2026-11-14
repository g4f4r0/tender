// Package client is the HTTP client for the hub's REST API, used by the agent and the tender command.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client sends JSON requests to the hub, with a bearer token when it has one.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New returns a client for the hub at base, such as https://apps.example.ts.net. An empty token
// sends no Authorization header, which is what the people routes (the tender command) take.
func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Error is an RFC 9457 problem answer from the hub.
type Error struct {
	Status int
	Title  string
	Detail string
	Type   string // the problem's type URI; about:blank for most
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("hub answered %d %s", e.Status, e.Title)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Retryable reports whether trying the same request again can succeed: network errors, 5xx,
// 408 and 429. Other 4xx answers are permanent.
func Retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Status == http.StatusRequestTimeout || e.Status == http.StatusTooManyRequests || e.Status >= 500
	}
	return true
}

// NewKey returns a random Idempotency-Key. Make one per logical request and reuse it on every retry.
func NewKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("client: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Do sends one request. in and out may be nil. A non-empty key is sent as Idempotency-Key.
func (c *Client) Do(ctx context.Context, method, path, key string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%s %s: encode body: %w", method, path, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return problem(method, path, resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode answer: %w", method, path, err)
	}
	return nil
}

func problem(method, path string, resp *http.Response) error {
	e := &Error{Status: resp.StatusCode, Title: http.StatusText(resp.StatusCode)}
	var p struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Type   string `json:"type"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(raw, &p) == nil {
		if p.Title != "" {
			e.Title = p.Title
		}
		e.Detail, e.Type = p.Detail, p.Type
	}
	return fmt.Errorf("%s %s: %w", method, path, e)
}
