package apalachebackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type transportRequest struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func transportResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// These HTTP fixtures exercise only the transport trust boundary, not semantic
// admission. Positive isolation-model tests use the real service in backend.
func transportClient(t *testing.T, handle func(http.ResponseWriter, *http.Request, transportRequest)) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request transportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Method == "health" {
			transportResult(w, request.ID, map[string]string{"status": "OK"})
			return
		}
		handle(w, r, request)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL+"/rpc?transport=1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestNewConfiguration(t *testing.T) {
	for _, endpoint := range []string{
		"", "://bad", "file:///rpc", "http:///rpc", "http://:8822/rpc",
		"http://user:secret@localhost/rpc", "http://localhost/rpc#fragment",
		"http://localhost/rpc#",
		" http://localhost/rpc", "http://localhost/rpc ", "http:rpc",
	} {
		t.Run(endpoint, func(t *testing.T) {
			if client, err := New(endpoint, time.Second); err == nil || client != nil {
				t.Fatalf("invalid URL accepted: client=%v error=%v", client, err)
			}
		})
	}
	if client, err := New("http://127.0.0.1/rpc", -time.Second); err == nil || client != nil {
		t.Fatalf("negative timeout accepted: client=%v error=%v", client, err)
	}
}

func TestNewHealth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result any
		ok     bool
	}{
		{"ok", map[string]any{"status": "OK"}, true},
		{"health is not version attestation", map[string]any{"status": "OK", "version": "not trusted"}, true},
		{"unhealthy", map[string]any{"status": "UNKNOWN"}, false},
		{"missing status", map[string]any{}, false},
		{"wrong status key", map[string]any{"Status": "OK"}, false},
		{"duplicate status", json.RawMessage(`{"status":"bad","status":"OK"}`), false},
		{"numeric status", map[string]any{"status": 1}, false},
		{"null", nil, false},
		{"array", []any{"OK"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request transportRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if request.Method != "health" || request.Version != "2.0" || string(request.ID) != "1" {
					t.Errorf("unexpected health request: %+v", request)
				}
				calls.Add(1)
				transportResult(w, request.ID, tc.result)
			}))
			defer server.Close()
			client, err := New(server.URL+"/rpc", 0)
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
			} else if err == nil || client != nil {
				t.Fatalf("invalid health accepted: client=%v error=%v", client, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("health requests=%d, want exactly one", calls.Load())
			}
		})
	}
	t.Run("RPC error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`)
		}))
		defer server.Close()
		if client, err := New(server.URL, time.Second); client != nil || err == nil || !strings.Contains(err.Error(), "-32601: Method not found") {
			t.Fatalf("health RPC error lost: client=%v error=%v", client, err)
		}
	})
}

func TestCallEnvelopeAndLosslessNumbers(t *testing.T) {
	client := transportClient(t, func(w http.ResponseWriter, r *http.Request, request transportRequest) {
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/rpc?transport=1" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("endpoint or HTTP request changed: %s %s %v", r.Method, r.URL.RequestURI(), r.Header)
		}
		if request.Version != "2.0" || request.Method != "transportProbe" || string(request.Params) != `{"value":9007199254740993}` {
			t.Errorf("unexpected RPC payload: %+v", request)
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"value":9007199254740993,"nested":[18446744073709551615]}}`, request.ID)
	})
	client.nextID = 9007199254740992
	var result map[string]any
	if err := client.Call(context.Background(), "transportProbe", map[string]any{"value": uint64(9007199254740993)}, &result); err != nil {
		t.Fatal(err)
	}
	if result["value"] != json.Number("9007199254740993") || result["nested"].([]any)[0] != json.Number("18446744073709551615") {
		t.Fatalf("numbers lost precision or type: %#v", result)
	}
}

func TestCallRejectsMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"wrong version", `{"jsonrpc":"1.0","id":2,"result":{}}`},
		{"missing version", `{"id":2,"result":{}}`},
		{"null version", `{"jsonrpc":null,"id":2,"result":{}}`},
		{"numeric version", `{"jsonrpc":2,"id":2,"result":{}}`},
		{"wrong ID", `{"jsonrpc":"2.0","id":3,"result":{}}`},
		{"missing ID", `{"jsonrpc":"2.0","result":{}}`},
		{"string ID", `{"jsonrpc":"2.0","id":"2","result":{}}`},
		{"negative ID", `{"jsonrpc":"2.0","id":-2,"result":{}}`},
		{"fractional ID", `{"jsonrpc":"2.0","id":2.0,"result":{}}`},
		{"exponential ID", `{"jsonrpc":"2.0","id":2e0,"result":{}}`},
		{"null ID", `{"jsonrpc":"2.0","id":null,"result":{}}`},
		{"overflow ID", `{"jsonrpc":"2.0","id":18446744073709551616,"result":{}}`},
		{"both", `{"jsonrpc":"2.0","id":2,"result":{},"error":{"code":1,"message":"bad"}}`},
		{"neither", `{"jsonrpc":"2.0","id":2}`},
		{"null error", `{"jsonrpc":"2.0","id":2,"error":null}`},
		{"array error", `{"jsonrpc":"2.0","id":2,"error":[]}`},
		{"missing code", `{"jsonrpc":"2.0","id":2,"error":{"message":"bad"}}`},
		{"string code", `{"jsonrpc":"2.0","id":2,"error":{"code":"1","message":"bad"}}`},
		{"fractional code", `{"jsonrpc":"2.0","id":2,"error":{"code":1.5,"message":"bad"}}`},
		{"overflow code", `{"jsonrpc":"2.0","id":2,"error":{"code":9223372036854775808,"message":"bad"}}`},
		{"missing message", `{"jsonrpc":"2.0","id":2,"error":{"code":1}}`},
		{"null message", `{"jsonrpc":"2.0","id":2,"error":{"code":1,"message":null}}`},
		{"numeric message", `{"jsonrpc":"2.0","id":2,"error":{"code":1,"message":42}}`},
		{"remote error", `{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found","data":{"detail":9007199254740993}}}`},
		{"null envelope", `null`},
		{"batch envelope", `[]`},
		{"empty body", ``},
		{"malformed", `{`},
		{"malformed value", `{"jsonrpc":!}`},
		{"malformed key", `{"jsonrpc":"2.0",42:0}`},
		{"unclosed envelope", `{"jsonrpc":"2.0"`},
		{"invalid UTF-8", "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":\"\xff\"}"},
		{"trailing value", `{"jsonrpc":"2.0","id":2,"result":{}} {}`},
		{"trailing junk", `{"jsonrpc":"2.0","id":2,"result":{}} garbage`},
		{"duplicate ID", `{"jsonrpc":"2.0","id":9,"id":2,"result":{}}`},
		{"duplicate result", `{"jsonrpc":"2.0","id":2,"result":{},"result":{}}`},
		{"duplicate error member", `{"jsonrpc":"2.0","id":2,"error":{"code":1,"code":2,"message":"bad"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := transportClient(t, func(w http.ResponseWriter, r *http.Request, request transportRequest) {
				_, _ = io.WriteString(w, tc.body)
			})
			var result any
			if err := client.Call(context.Background(), "transportProbe", nil, &result); err == nil {
				t.Fatalf("accepted malformed RPC response: %s", tc.body)
			}
		})
	}
}

func TestCallResultAndRequestErrors(t *testing.T) {
	var calls atomic.Int32
	client := transportClient(t, func(w http.ResponseWriter, r *http.Request, request transportRequest) {
		calls.Add(1)
		transportResult(w, request.ID, "not an object")
	})
	if err := client.Call(nil, "transportProbe", nil, nil); err == nil || !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("nil context: %v", err)
	}
	if err := client.Call(context.Background(), "", nil, nil); err == nil || !strings.Contains(err.Error(), "empty method") {
		t.Fatalf("empty method: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Call(ctx, "transportProbe", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cause lost: %v", err)
	}
	var unsupported *json.UnsupportedTypeError
	if err := client.Call(context.Background(), "transportProbe", make(chan int), nil); !errors.As(err, &unsupported) {
		t.Fatalf("request encoding cause lost: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached server: %d", calls.Load())
	}
	var result struct{}
	var mismatch *json.UnmarshalTypeError
	if err := client.Call(context.Background(), "transportProbe", nil, &result); !errors.As(err, &mismatch) {
		t.Fatalf("result decoding cause lost: %v", err)
	}
	var invalidTarget *json.InvalidUnmarshalError
	if err := client.Call(context.Background(), "transportProbe", nil, (*struct{})(nil)); !errors.As(err, &invalidTarget) {
		t.Fatalf("invalid result target cause lost: %v", err)
	}
	if err := client.Call(context.Background(), "transportProbe", nil, nil); err != nil {
		t.Fatalf("discarding valid result: %v", err)
	}
	client.nextID = ^uint64(0)
	if err := client.Call(context.Background(), "transportProbe", nil, nil); err == nil || !strings.Contains(err.Error(), "ID exhausted") {
		t.Fatalf("request ID wrapped: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected server requests: %d", calls.Load())
	}
}

func TestCallHTTPBoundaries(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer target.Close()
			client := transportClient(t, func(w http.ResponseWriter, r *http.Request, request transportRequest) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				transportResult(w, request.ID, map[string]any{})
			})
			if err := client.Call(context.Background(), "transportProbe", nil, nil); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP status %d", status)) {
				t.Fatalf("HTTP error lost: %v", err)
			}
			if redirected.Load() != 0 {
				t.Fatal("RPC followed a redirect")
			}
		})
	}
	t.Run("response byte limit", func(t *testing.T) {
		for _, extra := range []int{0, 1} {
			client := transportClient(t, func(w http.ResponseWriter, r *http.Request, request transportRequest) {
				body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{}}`, request.ID)
				_, _ = io.WriteString(w, body+strings.Repeat(" ", maxResponseBytes+extra-len(body)))
			})
			err := client.Call(context.Background(), "transportProbe", nil, nil)
			if extra == 0 && err != nil {
				t.Fatalf("exact limit rejected: %v", err)
			}
			if extra == 1 && (err == nil || !strings.Contains(err.Error(), "exceeds 1048576 bytes")) {
				t.Fatalf("oversize response not explicit: %v", err)
			}
		}
	})
	t.Run("truncated body", func(t *testing.T) {
		client := transportClient(t, func(w http.ResponseWriter, r *http.Request, request transportRequest) {
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, "{}")
		})
		if err := client.Call(context.Background(), "transportProbe", nil, nil); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("response read cause lost: %v", err)
		}
	})
	t.Run("lost response without retry", func(t *testing.T) {
		var attempts atomic.Int32
		client := transportClient(t, func(w http.ResponseWriter, r *http.Request, request transportRequest) {
			attempts.Add(1)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
		})
		if err := client.Call(context.Background(), "transportProbe", nil, nil); !errors.Is(err, io.EOF) {
			t.Fatalf("lost response cause lost: %v", err)
		}
		if attempts.Load() != 1 {
			t.Fatalf("lost response retried %d times", attempts.Load())
		}
	})
}

func TestCallCancellationAndTimeout(t *testing.T) {
	for _, mode := range []string{"caller cancellation", "caller deadline", "configured timeout"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			client := transportClient(t, func(w http.ResponseWriter, r *http.Request, request transportRequest) {
				close(entered)
				select {
				case <-r.Context().Done():
				case <-release:
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := context.Canceled
			if mode == "caller deadline" {
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
				want = context.DeadlineExceeded
			} else if mode == "configured timeout" {
				client.timeout = 100 * time.Millisecond
				want = context.DeadlineExceeded
			}
			finished := make(chan error, 1)
			go func() { finished <- client.Call(ctx, "transportProbe", nil, nil) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("request did not reach server")
			}
			if mode == "caller cancellation" {
				cancel()
			}
			select {
			case err := <-finished:
				if !errors.Is(err, want) {
					t.Fatalf("want preserved %v, got %v", want, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("request ignored cancellation or timeout")
			}
		})
	}
}

func TestCallConcurrentRequests(t *testing.T) {
	const count = 8
	arrived := make(chan string, count)
	release := make(chan struct{})
	client := transportClient(t, func(w http.ResponseWriter, r *http.Request, request transportRequest) {
		arrived <- string(request.ID)
		<-release
		transportResult(w, request.ID, map[string]any{})
	})
	finished := make(chan error, count)
	for range count {
		go func() { finished <- client.Call(context.Background(), "transportProbe", nil, nil) }()
	}
	ids := make(map[string]bool)
	for range count {
		select {
		case id := <-arrived:
			if ids[id] {
				close(release)
				t.Fatalf("duplicate concurrent request ID: %s", id)
			}
			ids[id] = true
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("independent RPC calls were serialized")
		}
	}
	close(release)
	for range count {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloseConnectionsAndCalls(t *testing.T) {
	closed := make(chan struct{}, 1)
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request transportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		transportResult(w, request.ID, map[string]string{"status": "OK"})
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	server.Start()
	defer server.Close()
	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not close owned idle HTTP connection")
	}
	if err := client.Call(context.Background(), "transportProbe", nil, nil); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("call after Close: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("closed transport sent another request: %d", calls.Load())
	}
}

func TestCloseDoesNotCancelStartedCall(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	client := transportClient(t, func(w http.ResponseWriter, r *http.Request, request transportRequest) {
		close(entered)
		<-release
		transportResult(w, request.ID, map[string]any{})
	})
	finished := make(chan error, 1)
	go func() { finished <- client.Call(context.Background(), "transportProbe", nil, nil) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("request did not start")
	}
	if err := client.Close(); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("Close canceled an already-started call: %v", err)
	}
}
