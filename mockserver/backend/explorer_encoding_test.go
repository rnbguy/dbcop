//go:build cgo

package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rnbguy/dbcop/mockserver/apalachebackend"
)

func TestApalacheLongReadPrefix(t *testing.T) {
	explorer := apalacheTestExplorer(t)
	const length = 24
	live := make([]TxnEvent, length)
	for i := range live {
		live[i] = TxnEvent{IsRead: true, Read: ReadRecord{Key: fmt.Sprintf("key%d", i%2), Initial: true}}
	}
	for iso := ReadCommitted; iso <= Serializable; iso++ {
		t.Run(fmt.Sprintf("%d/%s", length, levelName(iso)), func(t *testing.T) {
			request := StepRequest{
				Kind: "read", Isolation: iso, Session: 7, Key: "key0",
				Live: live, Candidates: []ReadSource{{Initial: true}},
			}
			result, err := explorer.Step(context.Background(), request)
			if err != nil || !result.Enabled || result.Source != request.Candidates[0] {
				t.Fatalf("valid full root-read prefix: result=%+v error=%v", result, err)
			}
			// A later source change is RC-valid but violates repeatability at every stronger level.
			request.Committed = []LoggedTxn{apalacheTestTxn(1, apalacheTestWrite("key0", 99))}
			request.Candidates = []ReadSource{{Version: 99}}
			result, err = explorer.Step(context.Background(), request)
			if err != nil || result.Enabled != (iso == ReadCommitted) {
				t.Fatalf("changed source after full prefix: result=%+v error=%v", result, err)
			}
			if result.Enabled && result.Source != request.Candidates[0] {
				t.Fatalf("source normalization lost original identity: %+v", result.Source)
			}
		})
	}
}

func TestApalacheWriteProjectionIntegrity(t *testing.T) {
	explorer := apalacheTestExplorer(t)
	first := apalacheTestTxn(1, apalacheTestRoot("x"), apalacheTestWrite("x", 1))
	last := apalacheTestTxn(2, apalacheTestRoot("x"), apalacheTestWrite("x", 2))
	input, err := renderApalacheRequest(StepRequest{
		Kind: "commit", Isolation: SnapshotIsolation, Session: last.Session,
		Committed: []LoggedTxn{first}, Transaction: last,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, projection, ok := strings.Cut(input.module, "ProjectedWrites == ")
	if !ok {
		t.Fatal("missing raw write projection")
	}
	projection, _, ok = strings.Cut(projection, "\n")
	if !ok || strings.Count(projection, "txn |->") != 2 {
		t.Fatal("expected both conflicting raw writes")
	}
	firstWrite, _, ok := strings.Cut(strings.TrimPrefix(projection, "<<"), "]],")
	if !ok {
		t.Fatal("missing first projected write")
	}
	firstWrite += "]]"
	omitted := "<<" + firstWrite + ">>"
	for _, fixture := range []struct {
		name, projection string
	}{
		{"omitted", omitted},
		{"duplicate", strings.TrimSuffix(projection, ">>") + "," + firstWrite + ">>"},
		{"wrong-content", strings.Replace(projection, "version |-> 1", "version |-> 99", 1)},
		{"extra", strings.TrimSuffix(projection, ">>") + "," + strings.Replace(firstWrite, "txn |-> 1", "txn |-> 99", 1) + ">>"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			corrupt := input
			corrupt.module = strings.Replace(input.module, "ProjectedWrites == "+projection, "ProjectedWrites == "+fixture.projection, 1)
			if apalacheEncodingSuccessor(t, explorer, corrupt, false) {
				t.Fatal("corrupt projection authorized a conflicting snapshot commit")
			}
		})
	}
	t.Run("effective-false-acceptance-control", func(t *testing.T) {
		corrupt := input
		corrupt.module = strings.Replace(input.module, "ProjectedWrites == "+projection, "ProjectedWrites == "+omitted, 1)
		if !apalacheEncodingSuccessor(t, explorer, corrupt, true) {
			t.Fatal("omission control did not expose the false acceptance prevented by validation")
		}
	})
}

func apalacheEncodingSuccessor(t *testing.T, explorer *apalacheExplorer, input apalacheInput, unchecked bool) bool {
	t.Helper()
	sources := apalachebackend.Sources(input.module)
	if unchecked {
		root, err := base64.StdEncoding.DecodeString(sources[0])
		if err != nil {
			t.Fatal(err)
		}
		validation := "            /\\ ValidWriteProjection(OrderHistory(selected'), ProjectedWrites)\n"
		if strings.Count(string(root), validation) != 1 {
			t.Fatal("missing projection validation in negative control")
		}
		sources[0] = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(root), validation, "", 1)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var load apalacheLoaded
	err := explorer.client.Call(ctx, "loadSpec", map[string]any{
		"sources": sources, "format": "tla", "init": "Init", "next": "Next", "exports": []string{"Result"},
	}, &load)
	if load.Session != "" {
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			var disposed struct {
				Session string `json:"sessionId"`
			}
			if err := explorer.client.Call(cleanup, "disposeSpec", map[string]any{"sessionId": load.Session}, &disposed); err != nil || disposed.Session != load.Session {
				t.Errorf("dispose projection probe %s: response=%+v error=%v", load.Session, disposed, err)
			}
		}()
	}
	if err != nil {
		t.Fatal(err)
	}
	init, next, err := apalacheTransitions(load)
	if err != nil {
		t.Fatal(err)
	}
	if enabled, err := explorer.assume(ctx, load.Session, init); err != nil || !enabled {
		t.Fatalf("projection probe initializer: enabled=%v error=%v", enabled, err)
	}
	if _, err := explorer.advance(ctx, load.Session, 1); err != nil {
		t.Fatal(err)
	}
	enabled, err := explorer.assume(ctx, load.Session, next)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		return false
	}
	if _, err := explorer.advance(ctx, load.Session, 2); err != nil {
		t.Fatal(err)
	}
	var query struct {
		Session string          `json:"sessionId"`
		Value   json.RawMessage `json:"operatorValue"`
	}
	if err := explorer.client.Call(ctx, "query", map[string]any{
		"sessionId": load.Session, "kinds": []string{"OPERATOR"}, "operator": "Result", "timeoutSec": explorer.solverTimeout(ctx),
	}, &query); err != nil {
		t.Fatal(err)
	}
	if query.Session != load.Session {
		t.Fatal("projection probe returned another session")
	}
	if _, err := input.decode(query.Value); err != nil {
		t.Fatal(err)
	}
	return true
}
