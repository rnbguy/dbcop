package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func apalacheWireURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("DBCOP_APALACHE_RPC")
	if url == "" {
		if os.Getenv("DBCOP_REQUIRE_APALACHE") == "1" {
			t.Fatal("DBCOP_APALACHE_RPC is required")
		}
		t.Skip("real Apalache wire tests require DBCOP_APALACHE_RPC")
	}
	return url
}

// The proxy records real RPC replies; it never manufactures a semantic successor.
type wireRPCLog struct {
	mu      sync.Mutex
	methods map[string]int
	active  map[string]bool
	kinds   map[string]int
	sources []string
	hold    chan struct{}
	reached chan struct{}
}

func wireRPCProxy(t *testing.T, target string) (*httptest.Server, *wireRPCLog) {
	t.Helper()
	log := &wireRPCLog{methods: make(map[string]int), active: make(map[string]bool), kinds: make(map[string]int)}
	client := &http.Client{Timeout: 40 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var request struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.mu.Lock()
		log.methods[request.Method]++
		log.mu.Unlock()
		forward, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		forward.Header.Set("Content-Type", "application/json")
		response, err := client.Do(forward)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		var envelope struct {
			Result struct {
				Session string          `json:"sessionId"`
				Value   json.RawMessage `json:"operatorValue"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		var hold chan struct{}
		if err := json.Unmarshal(data, &envelope); err == nil && len(envelope.Error) == 0 {
			log.mu.Lock()
			switch request.Method {
			case "loadSpec":
				log.active[envelope.Result.Session] = true
			case "disposeSpec":
				delete(log.active, envelope.Result.Session)
				if log.hold != nil {
					hold = log.hold
					log.hold = nil
					close(log.reached)
				}
			case "query":
				var value struct {
					Kind struct {
						Bigint string `json:"#bigint"`
					} `json:"kind"`
				}
				if err := json.Unmarshal(envelope.Result.Value, &value); err == nil {
					log.kinds[value.Kind.Bigint]++
					log.sources = append(log.sources, string(envelope.Result.Value))
				}
			}
			log.mu.Unlock()
		}
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(data)
	}))
	t.Cleanup(proxy.Close)
	return proxy, log
}

func (log *wireRPCLog) snapshot() (map[string]int, map[string]int) {
	log.mu.Lock()
	defer log.mu.Unlock()
	return maps.Clone(log.methods), maps.Clone(log.kinds)
}

func (log *wireRPCLog) assertDisposed(t *testing.T) {
	t.Helper()
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.active) != 0 || log.methods["loadSpec"] != log.methods["disposeSpec"] {
		t.Errorf("RPC sessions not disposed: active=%v methods=%v", log.active, log.methods)
	}
}

func startWireCommand(t *testing.T, rpcURL, isolation string) (*sql.DB, *sql.Conn, func()) {
	t.Helper()
	addr := unusedAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--addr", addr, "--iso", isolation, "--seed", "42", "--apalache-rpc", rpcURL, "--apalache-timeout", "30s"})
	}()
	stopped := false
	stop := func() {
		t.Helper()
		cancel()
		if stopped {
			return
		}
		select {
		case err := <-done:
			stopped = true
			if err != nil {
				t.Errorf("command shutdown: %v", err)
			}
		case <-time.After(45 * time.Second):
			t.Error("command did not drain SQL handlers during shutdown")
		}
	}
	t.Cleanup(stop)
	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(%s)/?timeout=250ms&readTimeout=2m&writeTimeout=5s", addr))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(3)
	// Closing a dedicated sql.Conn must disconnect, not return an open transaction to a pool.
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { _ = db.Close() })
	ready, readyCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer readyCancel()
	for {
		conn, err := db.Conn(ready)
		if err == nil {
			t.Cleanup(func() { _ = conn.Close() })
			return db, conn, stop
		}
		select {
		case serveErr := <-done:
			stopped = true
			t.Fatalf("command failed before accepting SQL: %v", serveErr)
		case <-ready.Done():
			t.Fatalf("command did not accept MySQL connections: %v (last connection error: %v)", ready.Err(), err)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func wireExec(t *testing.T, conn *sql.Conn, queries ...string) {
	t.Helper()
	for _, query := range queries {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err := conn.ExecContext(ctx, query)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
}

func wireRows(t *testing.T, conn *sql.Conn, want [][]int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rows, err := conn.QueryContext(ctx, "SELECT id,v FROM items ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][]int64
	for rows.Next() {
		var id, value int64
		if err := rows.Scan(&id, &value); err != nil {
			t.Fatal(err)
		}
		got = append(got, []int64{id, value})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire rows = %v, want %v", got, want)
	}
}

func TestApalacheWire(t *testing.T) {
	url := apalacheWireURL(t)
	for _, isolation := range []string{"read-committed", "repeatable-read", "atomic-read", "causal", "prefix", "snapshot-isolation", "serializable"} {
		t.Run(isolation, func(t *testing.T) {
			proxy, rpc := wireRPCProxy(t, url)
			db, conn, stop := startWireCommand(t, proxy.URL, isolation)
			before, _ := rpc.snapshot()
			if before["loadSpec"] != 1 || before["query"] != 1 {
				t.Fatalf("startup did not perform exactly one real capability query: %v", before)
			}
			rpc.assertDisposed(t)
			wireExec(t, conn,
				"CREATE DATABASE mbt", "USE mbt", "CREATE TABLE items (id INT PRIMARY KEY, v INT)",
				"INSERT INTO items VALUES (1,0),(2,0)", "CALL dbcop_seal_initial_state()")
			after, _ := rpc.snapshot()
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("conventional initialization/sealing used measured RPC: before=%v after=%v", before, after)
			}
			wireExec(t, conn, "SET dbcop_read_latest=1", "BEGIN", "UPDATE items SET v=1 WHERE id=1",
				"SAVEPOINT keep", "UPDATE items SET v=2 WHERE id=1", "ROLLBACK TO SAVEPOINT keep", "COMMIT")
			wireRows(t, conn, [][]int64{{1, 1}, {2, 0}})
			_, kinds := rpc.snapshot()
			for _, kind := range []string{"0", "1", "2"} {
				if kinds[kind] == 0 || kind == "0" && kinds[kind] == 1 {
					t.Fatalf("measured read/write/commit did not all query real successors: %v", kinds)
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			aborted, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = aborted.Close() })
			wireExec(t, aborted, "USE mbt", "SET dbcop_read_latest=1", "BEGIN", "UPDATE items SET v=99 WHERE id=2")
			if err := aborted.Close(); err != nil {
				t.Fatal(err)
			}
			wireRows(t, conn, [][]int64{{1, 1}, {2, 0}})
			rpc.assertDisposed(t)

			// Hold an actual operation's disposal reply while shutdown drains its SQL handler.
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer shutdownCancel()
			reached, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			rpc.mu.Lock()
			rpc.hold, rpc.reached = release, reached
			rpc.mu.Unlock()
			queryDone := make(chan error, 1)
			go func() {
				_, err := conn.ExecContext(shutdownCtx, "SELECT id,v FROM items ORDER BY id")
				queryDone <- err
			}()
			select {
			case <-reached:
			case err := <-queryDone:
				t.Fatalf("shutdown query returned before reaching real disposal: %v", err)
			case <-shutdownCtx.Done():
				t.Fatal(shutdownCtx.Err())
			}
			shutdown := make(chan struct{})
			go func() { stop(); close(shutdown) }()
			select {
			case <-shutdown:
				t.Error("command returned while an RPC-backed SQL handler was still draining")
			case <-time.After(100 * time.Millisecond):
			}
			unblock()
			select {
			case <-shutdown:
			case <-time.After(45 * time.Second):
				t.Fatal("shutdown did not finish after the RPC reply was released")
			}
			select {
			case <-queryDone:
			case <-time.After(5 * time.Second):
				t.Fatal("SQL handler outlived command shutdown")
			}
			if err := conn.PingContext(shutdownCtx); err == nil {
				t.Error("shutdown left a live MySQL connection")
			}
			rpc.assertDisposed(t)
			rpc.mu.Lock()
			t.Logf("backend=apalache required-model/tool=0.62.4 (operator provenance, not RPC-attested) isolation=%s client/store-seed=42 SMT-seed=unset selected-source-trace=%s", isolation, strings.Join(rpc.sources, ","))
			rpc.mu.Unlock()
		})
	}
}

func TestApalacheCommandListenerFailureCleansCapability(t *testing.T) {
	proxy, rpc := wireRPCProxy(t, apalacheWireURL(t))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := serve(context.Background(), listener.Addr().String(), "serializable", 1, proxy.URL, 30*time.Second); err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("occupied listener returned %v", err)
	}
	rpc.assertDisposed(t)
}
