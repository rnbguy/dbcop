package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func unusedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestApalacheCommandRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"isolation", []string{"--iso", "unknown", "--apalache-rpc", "invalid"}, "unknown isolation level"},
		{"negative-timeout", []string{"--apalache-timeout", "-1s"}, "timeout must be positive"},
		{"zero-timeout", []string{"--apalache-timeout", "0"}, "timeout must be positive"},
		{"malformed-timeout", []string{"--apalache-timeout", "soon"}, "invalid value"},
		{"invalid-rpc", []string{"--apalache-rpc", "invalid"}, "absolute HTTP(S) URL"},
		{"invalid-seed", []string{"--seed", "-1"}, "invalid value"},
		{"native-policy", []string{"--read-policy", "uniform"}, "flag provided but not defined"},
		{"native-deviation", []string{"--deviation-probability", "0.01"}, "flag provided but not defined"},
		{"positional", []string{"ignored"}, "unexpected positional argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := run(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%q) = %v, want %q", tc.args, err, tc.want)
			}
		})
	}
	if err := run(context.Background(), []string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help returned %v", err)
	}
}

func TestApalacheCommandCancelledBeforeStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serve(ctx, "not-an-address", "serializable", 1, "not-an-endpoint", time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestApalacheCommandStartupGate(t *testing.T) {
	for _, mode := range []string{"http-failure", "unhealthy", "missing-capability", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			addr := unusedAddress(t)
			var mu sync.Mutex
			var methods []string
			rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				mu.Lock()
				methods = append(methods, request.Method)
				mu.Unlock()
				if conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
					conn.Close()
					t.Error("SQL listener opened before the capability gate completed")
				}
				if mode == "http-failure" {
					http.Error(w, "explorer unavailable", http.StatusServiceUnavailable)
					return
				}
				if request.Method == "loadSpec" && mode == "timeout" {
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
					return
				}
				response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
				if request.Method == "health" {
					status := "OK"
					if mode == "unhealthy" {
						status = "NOT_OK"
					}
					response["result"] = map[string]string{"status": status}
				} else {
					response["error"] = map[string]any{"code": -32601, "message": "method not found"}
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			defer rpc.Close()
			timeout := time.Second
			if mode == "timeout" {
				timeout = 100 * time.Millisecond
			}
			err := serve(context.Background(), addr, "serializable", 1, rpc.URL, timeout)
			if err == nil {
				t.Fatal("unavailable explorer permitted command startup")
			}
			want := "apalache health"
			if mode == "missing-capability" || mode == "timeout" {
				want = "Apalache capability"
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("startup error = %v, want %q", err, want)
			}
			if mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout lost its cause: %v", err)
			}
			mu.Lock()
			got := strings.Join(methods, ",")
			mu.Unlock()
			wantMethods := "health"
			if mode == "missing-capability" || mode == "timeout" {
				wantMethods += ",loadSpec"
			}
			if got != wantMethods {
				t.Fatalf("RPC methods = %s, want %s; no fallback or retry is allowed", got, wantMethods)
			}
			if conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
				conn.Close()
				t.Fatal("failed command left a SQL listener open")
			}
		})
	}
}
