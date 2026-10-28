package apalachebackend

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout   = 30 * time.Second
	maxResponseBytes = 1 << 20
)

// Client is a concurrency-safe transport for the Apalache v0.62.4 explorer API.
// The caller owns exploration sessions; closing the transport does not dispose them.
type Client struct {
	url     string
	timeout time.Duration
	http    *http.Client

	mu     sync.Mutex
	nextID uint64
	closed bool
}

// New validates the endpoint with a real health request. Health does not attest
// the tool version; the operator must provision the pinned runtime separately.
// A zero timeout uses 30 seconds.
func New(rpcURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("apalache RPC URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.Contains(rpcURL, "#") || u.Opaque != "" || strings.TrimSpace(rpcURL) != rpcURL {
		return nil, errors.New("apalache RPC URL must be an absolute HTTP(S) URL without credentials or fragment")
	}
	if timeout < 0 {
		return nil, errors.New("apalache RPC timeout must not be negative")
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	c := &Client{
		url:     rpcURL,
		timeout: timeout,
		http: &http.Client{
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
			// Redirects must not change the configured endpoint or replay a POST.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	var health rpcHealth
	if err := c.Call(context.Background(), "health", nil, &health); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("apalache health: %w", err)
	}
	if health.Status != "OK" {
		_ = c.Close()
		return nil, errors.New("apalache health: expected status OK")
	}
	return c, nil
}

// Call makes one JSON-RPC request without retrying. A nil result discards the
// result value, but still requires a valid response envelope.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if ctx == nil {
		return errors.New("apalache RPC: nil context")
	}
	if method == "" {
		return errors.New("apalache RPC: empty method")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("apalache RPC %s: %w", method, err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("apalache RPC client is closed")
	}
	if c.nextID == ^uint64(0) {
		c.mu.Unlock()
		return errors.New("apalache RPC request ID exhausted")
	}
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	body, err := json.Marshal(struct {
		Version string `json:"jsonrpc"`
		ID      uint64 `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params})
	if err != nil {
		return fmt.Errorf("apalache RPC %s request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("apalache RPC %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("apalache RPC %s: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("apalache RPC %s response: %w", method, err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("apalache RPC %s response exceeds %d bytes", method, maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("apalache RPC %s: HTTP status %d", method, resp.StatusCode)
	}
	var envelope struct {
		Version string         `json:"jsonrpc"`
		ID      *uint64        `json:"id"`
		Result  jsontext.Value `json:"result"`
		Error   rpcError       `json:"error"`
	}
	if err := jsonv2.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("apalache RPC %s response: %w", method, err)
	}
	if envelope.Version != "2.0" {
		return fmt.Errorf("apalache RPC %s: invalid JSON-RPC version", method)
	}
	if envelope.ID == nil || *envelope.ID != id {
		return fmt.Errorf("apalache RPC %s: invalid or mismatched response ID", method)
	}
	if (envelope.Result != nil) == envelope.Error.present {
		return fmt.Errorf("apalache RPC %s: expected exactly one result or error", method)
	}
	if envelope.Error.present {
		if envelope.Error.Code == nil || envelope.Error.Message == nil {
			return fmt.Errorf("apalache RPC %s: invalid error code or message", method)
		}
		return fmt.Errorf("apalache RPC %s error %d: %s", method, *envelope.Error.Code, *envelope.Error.Message)
	}
	if result != nil {
		decoder := json.NewDecoder(bytes.NewReader(envelope.Result))
		decoder.UseNumber()
		if err := decoder.Decode(result); err != nil {
			return fmt.Errorf("apalache RPC %s result: %w", method, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("apalache RPC %s: %w", method, err)
	}
	return nil
}

// Close prevents new calls and closes owned idle connections. The owner must
// finish its active operations, including remote session disposal, first.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.http.CloseIdleConnections()
	}
	return nil
}

type rpcHealth struct {
	Status string `json:"status"`
}

func (h *rpcHealth) UnmarshalJSON(data []byte) error {
	type wire rpcHealth
	return jsonv2.Unmarshal(data, (*wire)(h))
}

type rpcError struct {
	Code    *int64  `json:"code"`
	Message *string `json:"message"`
	present bool
}

func (e *rpcError) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	type wire rpcError
	err := jsonv2.UnmarshalDecode(decoder, (*wire)(e))
	// Explicit null still counts as an error member, but lacks required fields.
	e.present = true
	return err
}
