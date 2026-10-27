//go:build cgo

package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dolthub/go-mysql-server/sql"
)

func TestApalacheCancellationDuringCommitDisposal(t *testing.T) {
	endpoint := apalacheTestURL(t)
	var armed atomic.Bool
	disposing, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
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
		forward, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		forward.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(forward)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if request.Method == "disposeSpec" && armed.CompareAndSwap(true, false) {
			close(disposing)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxy.Close()
	store, err := NewApalacheStore(1, 2, proxy.URL, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := NewSession(store, Serializable, 1)
	defer session.SessionEnd()
	session.beginTxn().stage(StagedWrite{Tab: "items", Pkey: "1", Col: "v", Value: int64(7)})
	ctx, cancel := context.WithCancel(context.Background())
	sqlContext := sql.NewContext(ctx, sql.WithSession(session))
	finished, stopped := make(chan error, 1), make(chan struct{})
	armed.Store(true)
	go func() {
		defer close(stopped)
		finished <- session.CommitTransaction(sqlContext, nil)
	}()
	defer func() {
		cancel()
		releaseOnce.Do(func() { close(release) })
		<-stopped
	}()
	select {
	case <-disposing:
	case err := <-finished:
		t.Fatalf("commit returned before disposal barrier: %v", err)
	case <-time.After(35 * time.Second):
		t.Fatal("commit did not reach real remote disposal")
	}
	cancel()
	releaseOnce.Do(func() { close(release) })
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled commit returned %v; published history=%+v", err, store.CommittedHistory())
	}
	if len(store.CommittedHistory()) != 0 || len(store.committedVersions(cellKey("items", "1", "v"))) != 0 {
		t.Fatal("commit cancelled during disposal published cells or history")
	}
	if session.liveTxn() != nil {
		t.Fatal("cancelled commit retained live transaction")
	}
	session.beginTxn().stage(StagedWrite{Tab: "items", Pkey: "1", Col: "v", Value: int64(8)})
	if err := session.CommitTransaction(sql.NewContext(context.Background(), sql.WithSession(session)), nil); err != nil {
		t.Fatalf("healthy successor after cancellation: %v", err)
	}
	versions := store.committedVersions(cellKey("items", "1", "v"))
	if len(versions) != 1 || versions[0].Value != int64(8) {
		t.Fatalf("cancelled commit contaminated its successor: %+v", versions)
	}
	verifyTLCObservations(t, []tlcObservation{{store.CommittedHistory(), Serializable, true}})
}
