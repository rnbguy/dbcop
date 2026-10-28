//go:build cgo

package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rnbguy/dbcop/mockserver/apalachebackend"
)

func apalacheTestURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("DBCOP_APALACHE_RPC")
	if url == "" {
		if os.Getenv("DBCOP_REQUIRE_APALACHE") == "1" {
			t.Fatal("DBCOP_APALACHE_RPC is required")
		}
		t.Skip("real Apalache service requires DBCOP_APALACHE_RPC")
	}
	return url
}

func apalacheTestExplorer(t *testing.T) *apalacheExplorer {
	t.Helper()
	e, err := newApalacheExplorer(apalacheTestURL(t), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Error(err)
		}
	})
	return e
}

func apalacheTestWrite(key string, id Version) LoggedEvent {
	return LoggedEvent{Write: &CommittedWrite{Key: key, ID: id}}
}
func apalacheTestRead(key string, id Version) LoggedEvent {
	return LoggedEvent{Read: &LoggedRead{Key: key, WriteID: id}}
}
func apalacheTestRoot(key string) LoggedEvent {
	return LoggedEvent{Read: &LoggedRead{Key: key, Initial: true}}
}
func apalacheTestTxn(session int, events ...LoggedEvent) LoggedTxn {
	txn := LoggedTxn{Session: session, Events: events}
	for _, event := range events {
		if event.Read != nil {
			txn.Reads = append(txn.Reads, *event.Read)
		}
		if event.Write != nil {
			txn.Writes = append(txn.Writes, *event.Write)
		}
	}
	return txn
}

// Expectations are the directed axioms used by the independent Rust/TLC gate,
// not copied answers from either executable checker.
func apalacheDirectedCases() []struct {
	name     string
	history  []LoggedTxn
	expected [7]bool
} {
	w, r, root, tx := apalacheTestWrite, apalacheTestRead, apalacheTestRoot, apalacheTestTxn
	all := [7]bool{true, true, true, true, true, true, true}
	return []struct {
		name     string
		history  []LoggedTxn
		expected [7]bool
	}{
		{"empty", nil, all},
		{"serial", []LoggedTxn{tx(1, w("x", 1)), tx(2, r("x", 1), w("y", 2))}, all},
		{"rc-not-rr", []LoggedTxn{tx(1, w("x", 1)), tx(2, w("x", 2)), tx(3, r("x", 1), r("x", 2))}, [7]bool{true}},
		{"rr-not-ra", []LoggedTxn{tx(1, w("x", 1)), tx(1, root("x"))}, [7]bool{true, true}},
		{"ra-not-cc", []LoggedTxn{tx(1, w("x", 1)), tx(2, r("x", 1), w("y", 2)), tx(3, r("y", 2), root("x"))}, [7]bool{true, true, true}},
		{"cc-not-pc", []LoggedTxn{tx(1, w("x", 1)), tx(2, w("y", 2)), tx(3, r("x", 1), root("y")), tx(4, r("y", 2), root("x"))}, [7]bool{true, true, true, true}},
		{"pc-not-si", []LoggedTxn{tx(1, root("x"), w("x", 1)), tx(2, root("x"), w("x", 2))}, [7]bool{true, true, true, true, true}},
		{"si-not-ser", []LoggedTxn{tx(1, root("y"), w("x", 1)), tx(2, root("x"), w("y", 2))}, [7]bool{true, true, true, true, true, true}},
		{"cross-variable-rc", []LoggedTxn{tx(1, w("x", 1), w("y", 2)), tx(2, r("y", 2), root("x"))}, [7]bool{}},
		{"cross-variable-reversed", []LoggedTxn{tx(1, w("x", 1), w("y", 2)), tx(2, root("x"), r("y", 2))}, [7]bool{true, true}},
		{"local-latest", []LoggedTxn{tx(1, w("x", 1), r("x", 1), w("x", 2), r("x", 2))}, all},
		{"explicit-zero", []LoggedTxn{tx(1, w("x", 0)), tx(1, r("x", 0))}, all},
		{"root-not-zero", []LoggedTxn{tx(1, w("x", 0)), tx(1, root("x"))}, [7]bool{true, true}},
		{"duplicate-write", []LoggedTxn{tx(1, w("x", 1)), tx(2, w("x", 1))}, [7]bool{}},
		{"missing-writer", []LoggedTxn{tx(1, r("x", 9))}, [7]bool{}},
		{"overwritten-external", []LoggedTxn{tx(1, w("x", 1), w("x", 2)), tx(2, r("x", 1))}, [7]bool{}},
		{"local-old", []LoggedTxn{tx(1, w("x", 1), w("x", 2), r("x", 1))}, [7]bool{}},
		{"local-future", []LoggedTxn{tx(1, r("x", 1), w("x", 1))}, [7]bool{}},
		{"root-after-local-zero", []LoggedTxn{tx(1, w("x", 0), root("x"))}, [7]bool{}},
		{"large-version", []LoggedTxn{tx(1, w("x", Version(math.MaxUint64))), tx(2, r("x", Version(math.MaxUint64)))}, all},
	}
}

func TestApalacheModelGate(t *testing.T) {
	e := apalacheTestExplorer(t)
	var observations []tlcObservation
	for _, fixture := range apalacheDirectedCases() {
		for iso := ReadCommitted; iso <= Serializable; iso++ {
			t.Run(fixture.name+"/"+levelName(iso), func(t *testing.T) {
				request := StepRequest{Kind: "commit", Isolation: iso, Session: 0, Transaction: LoggedTxn{Session: 0}}
				if len(fixture.history) > 0 {
					last := fixture.history[len(fixture.history)-1]
					request.Session, request.Transaction = last.Session, last
					request.Committed = fixture.history[:len(fixture.history)-1]
				}
				result, err := e.Step(context.Background(), request)
				if err != nil {
					t.Fatalf("history=%+v: %v", fixture.history, err)
				}
				if result.Enabled != fixture.expected[iso] {
					t.Fatalf("history=%+v: enabled=%v expected=%v", fixture.history, result.Enabled, fixture.expected[iso])
				}
				native := checkLevel(buildSessions(fixture.history, request.Session, nil), levelName(iso))
				if native != result.Enabled {
					t.Fatalf("native=%v Apalache=%v history=%+v", native, result.Enabled, fixture.history)
				}
				observations = append(observations, tlcObservation{fixture.history, iso, result.Enabled})
			})
		}
	}
	verifyTLCObservations(t, observations)
	t.Run("local-and-write-steps", func(t *testing.T) {
		write := StagedWrite{Tab: "items", Pkey: "1", Col: "x", Value: "not sent to model"}
		other := StagedWrite{Tab: "items", Pkey: "1", Col: "y", Value: nil}
		live := []TxnEvent{{Write: write}, {Write: other}, {Write: write}}
		for iso := ReadCommitted; iso <= Serializable; iso++ {
			request := StepRequest{Kind: "read", Isolation: iso, Session: 7, Key: cellKey("items", "1", "x"), Live: live, Candidates: []ReadSource{{Local: true, Version: 3}}}
			result, err := e.Step(context.Background(), request)
			if err != nil || !result.Enabled || result.Source != request.Candidates[0] {
				t.Fatalf("local %s: %+v %v", levelName(iso), result, err)
			}
			request.Candidates[0].Version = 1
			result, err = e.Step(context.Background(), request)
			if err != nil || result.Enabled {
				t.Fatalf("old local %s: %+v %v", levelName(iso), result, err)
			}
			request.Candidates = []ReadSource{{Initial: true}}
			result, err = e.Step(context.Background(), request)
			if err != nil || result.Enabled {
				t.Fatalf("external source bypassed local write: %+v %v", result, err)
			}
			// Even a commit-inconsistent read/write prefix may stage another write.
			request.Kind, request.Write = "write", write
			request.Live = []TxnEvent{{IsRead: true, Read: ReadRecord{Key: "missing", WriteID: 99}}, {Write: write}}
			request.Candidates = nil
			result, err = e.Step(context.Background(), request)
			if err != nil || !result.Enabled {
				t.Fatalf("write was checked as premature commit: %+v %v", result, err)
			}
		}
	})
	t.Run("cancellation-is-not-rejection", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request := StepRequest{Kind: "read", Isolation: Serializable, Session: 9, Key: "absent", Candidates: []ReadSource{{Initial: true}}}
		result, err := e.Step(ctx, request)
		if result.Enabled || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation became a decision: %+v %v", result, err)
		}
		result, err = e.Step(context.Background(), request)
		if err != nil || !result.Enabled || !result.Source.Initial {
			t.Fatalf("cancelled operation contaminated its successor: %+v %v", result, err)
		}
	})
	t.Run("constructor-and-close", func(t *testing.T) {
		store, err := NewApalacheStore(1, 2, apalacheTestURL(t), 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if store.oracles.factory != nil || len(store.oracles.states) != 0 {
			t.Fatal("Apalache constructor created native state")
		}
		explorer := store.oracles.explorer
		if err := explorer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := explorer.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := explorer.Step(context.Background(), StepRequest{Kind: "read"}); err == nil {
			t.Fatal("closed explorer admitted a new operation")
		}
	})
}

func TestNativeApalacheAgreement(t *testing.T) {
	e := apalacheTestExplorer(t)
	var observations []tlcObservation
	for iso := ReadCommitted; iso <= Serializable; iso++ {
		t.Run("source-sets/"+levelName(iso), func(t *testing.T) {
			store, session := liveOracleFixture(t, iso)
			key := cellKey("items", "1", "x")
			v := store.Write("items", "1", "x", int64(30))
			store.appendLog(1, store.nextSeq(), []CommittedWrite{{Key: key, ID: v}}, nil)
			txn := session.beginTxn()
			for _, prefix := range []string{"empty", "read-x", "read-y"} {
				t.Run(prefix, func(t *testing.T) {
					txn.restore(txnCheckpoint{})
					if prefix != "empty" {
						column, id := "x", Version(1)
						if prefix == "read-y" {
							column, id = "y", 2
						}
						txn.logRead(ReadRecord{Session: session.sid, Key: cellKey("items", "1", column), WriteID: id, Found: true})
					}
					request := StepRequest{Kind: "read", Isolation: iso, Session: session.sid, Committed: store.CommittedHistory(), Live: liveEvents(session), Key: key, Candidates: []ReadSource{{Initial: true}, {Version: 1}, {Version: v}}}
					want := make(map[ReadSource]bool)
					for _, source := range request.Candidates {
						if session.admits(key, source.Version, source.Initial) {
							want[source] = true
						}
					}
					got, err := enumerateApalacheSources(e, request)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("history=%+v live=%+v source set=%v native=%v", request.Committed, request.Live, got, want)
					}
					for _, source := range request.Candidates {
						observations = append(observations, tlcObservation{externalReadHistory(request, source), iso, got[source]})
					}
					request.Latest = true
					result, err := e.Step(context.Background(), request)
					if err != nil {
						t.Fatal(err)
					}
					latest := ReadSource{Initial: true}
					for source := range want {
						if !source.Initial && (latest.Initial || source.Version > latest.Version) {
							latest = source
						}
					}
					if result.Enabled != (len(want) > 0) || result.Enabled && result.Source != latest {
						t.Fatalf("latest=%+v want=%+v error=%v", result, latest, err)
					}
					// The actual returned source, not an auxiliary order witness, becomes the next observation.
					request.Latest = false
					result, err = e.Step(context.Background(), request)
					if err != nil || !result.Enabled || !want[result.Source] {
						t.Fatalf("selected model not admitted: %+v %v", result, err)
					}
					next := append(append([]TxnEvent(nil), request.Live...), TxnEvent{IsRead: true, Read: ReadRecord{Key: key, WriteID: result.Source.Version, Initial: result.Source.Initial}})
					if !checkLevel(buildSessions(request.Committed, request.Session, next), levelName(iso)) {
						t.Fatal("actual selected history failed native checker")
					}
				})
			}
		})
	}
	t.Run("invalid-latest-source-universe", func(t *testing.T) {
		for _, sources := range [][]ReadSource{
			{{}}, {{Initial: true, Version: 1}}, {{Local: true, Initial: true}},
			{{Local: true}}, {{Initial: true}, {Initial: true}},
		} {
			request := StepRequest{Kind: "read", Isolation: ReadCommitted, Key: "x", Latest: true, Candidates: sources}
			if _, err := e.Step(context.Background(), request); err == nil {
				t.Fatalf("latest read accepted malformed source identities: %+v", sources)
			}
		}
	})
	verifyTLCObservations(t, observations)
}

func enumerateApalacheSources(e *apalacheExplorer, request StepRequest) (sources map[ReadSource]bool, err error) {
	sources = make(map[ReadSource]bool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = e.successor(ctx, request, func(session string, snapshot int, input apalacheInput, first StepResult) (err error) {
		sources[first.Source] = true // successor requires a successful initial Result query.
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var response apalachePosition
			restoreErr := e.client.Call(cleanup, "rollback", map[string]any{"sessionId": session, "snapshotId": snapshot}, &response)
			if restoreErr == nil && (response.Session != session || response.Snapshot == nil || *response.Snapshot != snapshot) {
				restoreErr = fmt.Errorf("invalid enumeration rollback identity")
			}
			err = errors.Join(err, restoreErr)
		}()
		// Result excludes rank, so each nextModel blocks one source, not one ranking.
		for count := 0; count <= len(request.Candidates); count++ {
			var response struct {
				Session string          `json:"sessionId"`
				Old     json.RawMessage `json:"oldValue"`
				HasOld  string          `json:"hasOld"`
				HasNext string          `json:"hasNext"`
			}
			if err = e.client.Call(ctx, "nextModel", map[string]any{"sessionId": session, "operator": "Result", "timeoutSec": e.solverTimeout(ctx)}, &response); err != nil {
				return err
			}
			if response.Session != session || response.HasOld != "TRUE" {
				return fmt.Errorf("inconclusive nextModel old status %q", response.HasOld)
			}
			old, decodeErr := input.decode(response.Old)
			if decodeErr != nil {
				return decodeErr
			}
			sources[old.Source] = true
			switch response.HasNext {
			case "FALSE":
				return nil
			case "TRUE":
			default:
				return fmt.Errorf("inconclusive nextModel next status %q", response.HasNext)
			}
		}
		return fmt.Errorf("nextModel exceeded finite source universe")
	})
	return sources, err
}

func TestApalacheTranslationBoundaries(t *testing.T) {
	request := StepRequest{Kind: "read", Isolation: Causal, Session: math.MaxInt, Key: "SQL ' text \n never interpolated", Candidates: []ReadSource{{Version: Version(math.MaxUint64)}}}
	request.Committed = []LoggedTxn{apalacheTestTxn(8, apalacheTestWrite(request.Key, Version(math.MaxUint64)))}
	input, err := renderApalacheRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input.module, request.Key) || strings.Contains(input.module, "18446744073709551615") {
		t.Fatal("request leaked SQL text or failed numeric normalization")
	}
	for _, raw := range []string{`{"#bigint":"9007199254740993"}`, `{"#bigint":"18446744073709551615"}`} {
		if _, err := apalacheInteger(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`1`, `{"#bigint":"-1"}`, `{"#bigint":"18446744073709551616"}`, `{"#bigint":1}`, `{"#bigint":"1.0"}`, `{"#bigint":""}`, `{"#bigint":"1","extra":0}`} {
		if _, err := apalacheInteger(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid ITF integer %s", raw)
		}
	}
	decoded, err := input.decode(json.RawMessage(`{"kind":{"#bigint":"0"},"version":{"#bigint":"1"},"initial":false,"local":false}`))
	if err != nil || decoded.Source.Version != Version(math.MaxUint64) {
		t.Fatalf("lossy source translation: %+v %v", decoded, err)
	}
	for _, raw := range []string{`{}`, `{"kind":{"#bigint":"0"},"version":{"#bigint":"99"},"initial":false,"local":false}`, `{"kind":{"#bigint":"0"},"version":{"#bigint":"1"},"initial":null,"local":false}`, `{"kind":{"#bigint":"1"},"version":{"#bigint":"1"},"initial":false,"local":false}`} {
		if _, err := input.decode(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid Result %s", raw)
		}
	}
	metadata := `{"sessionId":"a","snapshotId":0,"specParameters":{"initTransitions":[{"index":0,"labels":[]}],"nextTransitions":[{"index":0,"labels":["TakeStep"]}]}}`
	var load apalacheLoaded
	if err := json.Unmarshal([]byte(metadata), &load); err != nil {
		t.Fatal(err)
	}
	if _, _, err := apalacheTransitions(load); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(metadata, `"sessionId":"a"`, `"sessionId":"-1"`, 1),
		strings.Replace(metadata, `"snapshotId":0`, `"snapshotId":-1`, 1),
		strings.Replace(metadata, `"index":0`, `"index":9`, 1),
		strings.Replace(metadata, `["TakeStep"]`, `[]`, 1),
		strings.Replace(metadata, `["TakeStep"]`, `["TakeStep","TakeStep"]`, 1),
		strings.Replace(metadata, `[{"index":0,"labels":[]}]`, `[]`, 1),
	} {
		var invalid apalacheLoaded
		if err := json.Unmarshal([]byte(bad), &invalid); err != nil {
			t.Fatal(err)
		}
		if _, _, err := apalacheTransitions(invalid); err == nil {
			t.Fatalf("accepted invalid transition metadata %s", bad)
		}
	}
	for _, mutate := range []func(*StepRequest){
		func(r *StepRequest) { r.Kind = "nextState" }, func(r *StepRequest) { r.Isolation = 99 }, func(r *StepRequest) { r.Session = -1 },
		func(r *StepRequest) { r.Candidates = []ReadSource{{Initial: true, Local: true}} },
		func(r *StepRequest) { r.Candidates = []ReadSource{{Local: true}} },
		func(r *StepRequest) { r.Candidates = []ReadSource{{}} },
		func(r *StepRequest) { r.Candidates = append(r.Candidates, r.Candidates[0]) },
		func(r *StepRequest) { r.Committed = []LoggedTxn{{Session: 0, Events: []LoggedEvent{{}}}} },
	} {
		bad := request
		mutate(&bad)
		if _, err := renderApalacheRequest(bad); err == nil {
			t.Fatal("accepted malformed explorer request")
		}
	}
}

func TestApalacheDomainFailureBoundaries(t *testing.T) {
	// This fixture never supplies an enabled semantic successor: it injects UNKNOWN
	// and a failed cleanup to test the trust boundary, not semantic correctness.
	disposing, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var assumptions, advances, queries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var result any
		switch request.Method {
		case "health":
			result = map[string]any{"status": "OK"}
		case "loadSpec":
			result = map[string]any{"sessionId": "a", "snapshotId": 0, "specParameters": map[string]any{
				"initTransitions": []any{map[string]any{"index": 0, "labels": []string{}}},
				"nextTransitions": []any{map[string]any{"index": 0, "labels": []string{"TakeStep"}}},
			}}
		case "assumeTransition":
			status := "ENABLED"
			if assumptions.Add(1) > 1 {
				status = "UNKNOWN"
			}
			result = map[string]any{"sessionId": "a", "snapshotId": 2, "transitionId": 0, "status": status}
		case "nextStep":
			advances.Add(1)
			result = map[string]any{"sessionId": "a", "snapshotId": 3, "newStepNo": 1}
		case "query":
			queries.Add(1)
			http.Error(w, "unexpected query", http.StatusInternalServerError)
			return
		case "disposeSpec":
			close(disposing)
			<-release
			http.Error(w, "cleanup unavailable", http.StatusServiceUnavailable)
			return
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer func() { releaseOnce.Do(func() { close(release) }); server.Close() }()
	client, err := apalachebackend.New(server.URL, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	explorer := &apalacheExplorer{client: client, timeout: 10 * time.Second, closeDone: make(chan struct{})}
	stepDone := make(chan error, 1)
	go func() {
		result, err := explorer.Step(context.Background(), StepRequest{Kind: "read", Isolation: Serializable, Key: "x", Candidates: []ReadSource{{Initial: true}}})
		if result.Enabled {
			stepDone <- fmt.Errorf("UNKNOWN became enabled")
			return
		}
		stepDone <- err
	}()
	select {
	case <-disposing:
	case <-time.After(5 * time.Second):
		t.Fatal("UNKNOWN failed to reach cleanup")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- explorer.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close did not drain active disposal: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	err = <-stepDone
	if err == nil || !strings.Contains(err.Error(), "UNKNOWN") || !strings.Contains(err.Error(), "dispose Apalache session a") {
		t.Fatalf("primary/cleanup cause lost: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := explorer.Close(); err != nil {
		t.Fatal(err)
	}
	if advances.Load() != 1 || queries.Load() != 0 {
		t.Fatal("UNKNOWN advanced or queried a semantic result")
	}
	if _, err := explorer.Step(context.Background(), StepRequest{}); err == nil {
		t.Fatal("closed explorer accepted a request")
	}
}
