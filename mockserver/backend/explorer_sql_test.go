//go:build cgo

package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/rnbguy/dbcop/mockserver/apalachebackend"
)

// This wrapper restricts request data only; every decision comes from the real explorer.
type sqlExplorerRecorder struct {
	real         Explorer
	force        map[string]ReadSource
	before       func(context.Context, StepRequest) (context.Context, func())
	shutdown     func()
	requests     []StepRequest
	results      []StepResult
	observations []tlcObservation
}

func (r *sqlExplorerRecorder) Step(ctx context.Context, request StepRequest) (StepResult, error) {
	if request.Kind == "read" && len(request.Candidates) > 0 && !request.Candidates[0].Local {
		if source, ok := r.force[request.Key]; ok {
			found := false
			for _, candidate := range request.Candidates {
				found = found || candidate == source
			}
			if !found {
				return StepResult{}, fmt.Errorf("test forced source outside actual SQL universe: %+v", source)
			}
			request.Candidates = []ReadSource{source}
			request.Latest = false
		}
	}
	if r.before != nil {
		var finish func()
		ctx, finish = r.before(ctx, request)
		defer finish()
	}
	result, err := r.real.Step(ctx, request)
	r.requests = append(r.requests, request)
	r.results = append(r.results, result)
	if err == nil {
		if request.Kind == "read" && result.Enabled && !result.Source.Local {
			r.observations = append(r.observations, tlcObservation{externalReadHistory(request, result.Source), request.Isolation, true})
		}
		if request.Kind == "commit" {
			history := append(append([]LoggedTxn(nil), request.Committed...), request.Transaction)
			r.observations = append(r.observations, tlcObservation{history, request.Isolation, result.Enabled})
		}
	}
	return result, err
}
func (r *sqlExplorerRecorder) Close() error { return r.real.Close() }

func newSQLExplorerFixture(t *testing.T, iso Isolation, url string) (*sqlTableFixture, *sqlExplorerRecorder) {
	t.Helper()
	RegisterVariables()
	real, err := newApalacheExplorer(url, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &sqlExplorerRecorder{real: real, force: make(map[string]ReadSource)}
	store, err := newStoreWithExplorer(1, 2, recorder)
	if err != nil {
		_ = real.Close()
		t.Fatal(err)
	}
	provider := NewProvider(store, iso, nil)
	engine := NewEngine(provider)
	session := NewSession(store, iso, 1)
	ctx := sql.NewContext(context.Background(), sql.WithSession(session))
	f := &sqlTableFixture{store: store, provider: provider, engine: engine, ctx: ctx}
	var closeOnce sync.Once
	recorder.shutdown = func() {
		closeOnce.Do(func() {
			session.SessionEnd()
			if err := engine.Close(); err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
			store.Close()
		})
	}
	t.Cleanup(recorder.shutdown)
	t.Cleanup(func() {
		if t.Failed() && len(recorder.requests) != 0 {
			input, err := renderApalacheRequest(recorder.requests[len(recorder.requests)-1])
			if err != nil {
				t.Logf("failed request encoding: %v", err)
				return
			}
			data, err := json.Marshal(map[string]any{
				"sources": apalachebackend.Sources(input.module), "format": "tla",
				"init": "Init", "next": "Next", "exports": []string{"Result"},
			})
			if err != nil {
				t.Logf("failed request serialization: %v", err)
				return
			}
			t.Logf("APALACHE_REPRO|%s", data)
		}
	})
	if err := store.BeginInitialization(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"CREATE DATABASE explorer_sql", "USE explorer_sql",
		"CREATE TABLE items (id INT PRIMARY KEY, v INT UNIQUE, n INT)",
		"INSERT INTO items VALUES (1,10,NULL),(2,20,200)",
	} {
		f.query(t, query)
	}
	f.query(t, "CALL dbcop_seal_initial_state()")
	if len(recorder.requests) != 0 || len(store.CommittedHistory()) != 0 {
		t.Fatal("initial loading performed measured steps or published measured history")
	}
	f.query(t, "SET dbcop_read_latest=1")
	return f, recorder
}

func sqlExplorerPeer(t *testing.T, f *sqlTableFixture, sid int) *sqlTableFixture {
	t.Helper()
	session := NewSession(f.store, f.provider.iso, sid)
	ctx := sql.NewContext(context.Background(), sql.WithSession(session))
	peer := &sqlTableFixture{store: f.store, provider: f.provider, engine: f.engine, ctx: ctx}
	t.Cleanup(session.SessionEnd)
	peer.query(t, "USE explorer_sql")
	peer.query(t, "SET dbcop_read_latest=1")
	return peer
}

func sqlExplorerKey(t *testing.T, f *sqlTableFixture, pkey, column string) string {
	t.Helper()
	db, err := f.provider.Database(f.ctx, "explorer_sql")
	if err != nil {
		t.Fatal(err)
	}
	table, ok, err := db.GetTableInsensitive(f.ctx, "items")
	if err != nil || !ok {
		t.Fatalf("items: %v", err)
	}
	return cellKey(table.(*Table).storageName, pkey, column)
}

func sqlExplorerValidate(t *testing.T, r *sqlExplorerRecorder) []tlcObservation {
	t.Helper()
	for i, request := range r.requests {
		result := r.results[i]
		written := make(map[string]bool)
		for _, event := range request.Live {
			if event.IsRead {
				if written[event.Read.Key] {
					t.Fatal("local read leaked into the external SQL event stream")
				}
			} else {
				written[cellKey(event.Write.Tab, event.Write.Pkey, event.Write.Col)] = true
			}
		}
		if !result.Enabled || !result.Source.Local {
			continue
		}
		position, latest := Version(0), Version(0)
		for _, event := range request.Live {
			if event.IsRead {
				continue
			}
			position++
			if cellKey(event.Write.Tab, event.Write.Pkey, event.Write.Col) == request.Key {
				latest = position
			}
		}
		if request.Kind != "read" || latest == 0 || result.Source != (ReadSource{Local: true, Version: latest}) {
			t.Fatalf("local SQL read did not select latest staged write: %+v result=%+v", request, result)
		}
	}
	for _, observation := range r.observations {
		if got := checkLevel(buildSessions(observation.history, 0, nil), levelName(observation.level)); got != observation.enabled {
			t.Fatalf("native disagrees with actual SQL history at %s: expected %v history=%+v", levelName(observation.level), observation.enabled, observation.history)
		}
	}
	return r.observations
}

func sqlExplorerProvenance(t *testing.T, f *sqlTableFixture, r *sqlExplorerRecorder) {
	t.Helper()
	var selected []Admission
	for i, request := range r.requests {
		result := r.results[i]
		if request.Kind == "read" && result.Enabled && !result.Source.Local {
			selected = append(selected, Admission{Key: request.Key, Admitted: -1, Selected: result.Source.Version, Initial: result.Source.Initial})
		}
	}
	got := f.store.Admissions()
	if len(got) != len(selected) {
		t.Fatalf("external admissions=%+v actual selections=%+v", got, selected)
	}
	for i, admission := range got {
		want := selected[i]
		if admission.Key != want.Key || admission.Admitted != -1 || admission.Selected != want.Selected || admission.Initial != want.Initial {
			t.Fatalf("admission %d lost selected provenance: %+v want %+v", i, admission, want)
		}
	}
	t.Logf("backend=apalache isolation=%s store-seed=1/2 (not SMT seed) tool=externally-provisioned-v0.62.4 selected-source-trace=%+v", levelName(f.provider.iso), got)
}

func TestApalacheSQLMatrix(t *testing.T) {
	url := apalacheTestURL(t)
	var observations []tlcObservation
	for _, iso := range namedSavepointLevels {
		t.Run(levelName(iso), func(t *testing.T) {
			t.Run("root-local-savepoints-statement-disconnect", func(t *testing.T) {
				proxy := newSQLRPCProxy(t, url)
				f, recorder := newSQLExplorerFixture(t, iso, proxy.server.URL)
				f.query(t, "BEGIN")
				namedSavepointRows(t, f, "SELECT v,n FROM items WHERE id=1", []sql.Row{{int32(10), nil}})
				namedSavepointRows(t, f, "SELECT v FROM items WHERE id=99", nil)
				for _, query := range []string{"UPDATE items SET v=11 WHERE id=1", "UPDATE items SET v=12 WHERE id=1"} {
					f.query(t, query)
				}
				txn := f.ctx.Session.(*Session).liveTxn()
				beforeLocal := txn.checkpoint()
				namedSavepointRows(t, f, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(12)}})
				// Existence/id may be external reads; v itself must remain a local observation.
				local := false
				for i, request := range recorder.requests {
					local = local || request.Kind == "read" && request.Key == sqlExplorerKey(t, f, "1", "v") && recorder.results[i].Source.Local
				}
				if !local || txn.checkpoint().writes != beforeLocal.writes {
					t.Fatal("read-your-writes bypassed explorer or staged another write")
				}
				f.query(t, "SAVEPOINT keep")
				prefix, events, members := txn.checkpoint(), txn.eventsSnapshot(), txn.memberSnapshot()
				for _, query := range []string{"UPDATE items SET v=13 WHERE id=1", "INSERT INTO items VALUES (3,30,300)", "DELETE FROM items WHERE id=2", "SAVEPOINT later", "ROLLBACK TO keep"} {
					f.query(t, query)
				}
				if txn.checkpoint() != prefix || !reflect.DeepEqual(txn.eventsSnapshot(), events) || !reflect.DeepEqual(txn.memberSnapshot(), members) {
					t.Fatal("savepoint did not restore exact ordered prefix")
				}
				namedSavepointMissing(t, f, "ROLLBACK TO later")
				f.query(t, "RELEASE SAVEPOINT keep")
				namedSavepointMissing(t, f, "ROLLBACK TO keep")
				// A real constraint error after staging row 3 restores the statement, not the earlier prefix.
				if err := foreignKeyQueryError(f, "INSERT INTO items VALUES (3,30,300),(4,20,400)"); err == nil {
					t.Fatal("duplicate unique value was accepted")
				}
				if txn.checkpoint() != prefix || !reflect.DeepEqual(txn.eventsSnapshot(), events) || !reflect.DeepEqual(txn.memberSnapshot(), members) {
					t.Fatal("failed SQL statement changed retained prefix")
				}
				f.query(t, "COMMIT")
				namedSavepointPublished(t, f.store, iso, events)
				namedSavepointRows(t, f, "SELECT id,v,n FROM items ORDER BY id", []sql.Row{{int32(1), int32(12), nil}, {int32(2), int32(20), int32(200)}})
				peer := sqlExplorerPeer(t, f, 2)
				peer.query(t, "BEGIN")
				peer.query(t, "INSERT INTO items VALUES (9,90,900)")
				before := f.store.CommittedHistory()
				peer.ctx.Session.(*Session).SessionEnd()
				assertSessionTransactionEnded(t, peer.ctx.Session.(*Session))
				if !reflect.DeepEqual(before, f.store.CommittedHistory()) {
					t.Fatal("disconnect published abandoned work")
				}
				namedSavepointRows(t, f, "SELECT v FROM items WHERE id=9", nil)
				db, err := f.provider.Database(f.ctx, "explorer_sql")
				if err != nil {
					t.Fatal(err)
				}
				raw, ok, err := db.GetTableInsensitive(f.ctx, "items")
				if err != nil || !ok {
					t.Fatalf("items: %v", err)
				}
				table := raw.(*Table)
				beforeMetadata := len(recorder.requests)
				table.latestCell(f.ctx.Session.(*Session), "1", "v")
				table.latestRow(f.ctx.Session.(*Session), "1")
				table.knownKeys(f.ctx.Session.(*Session))
				if len(recorder.requests) != beforeMetadata {
					t.Fatal("physical metadata probes performed semantic RPC")
				}
				hasRead, hasCell, hasMembership, hasCommit := false, false, false, false
				for i, request := range recorder.requests {
					if !recorder.results[i].Enabled {
						continue
					}
					hasRead = hasRead || request.Kind == "read"
					hasCell = hasCell || request.Kind == "write" && request.Write.Col == "v"
					hasMembership = hasMembership || request.Kind == "write" && request.Write.Col == rowExistenceColumn
					hasCommit = hasCommit || request.Kind == "commit"
				}
				if !hasRead || !hasCell || !hasMembership || !hasCommit {
					t.Fatal("SQL path bypassed a measured read/cell/membership/commit step")
				}
				sqlExplorerProvenance(t, f, recorder)
				observations = append(observations, sqlExplorerValidate(t, recorder)...)
			})
			t.Run("historical-mixed-cells", func(t *testing.T) {
				proxy := newSQLRPCProxy(t, url)
				f, recorder := newSQLExplorerFixture(t, iso, proxy.server.URL)
				writer := sqlExplorerPeer(t, f, 2)
				for _, value := range []int{11, 12} {
					writer.query(t, "BEGIN")
					writer.query(t, fmt.Sprintf("UPDATE items SET v=%d WHERE id=1", value))
					writer.query(t, "COMMIT")
				}
				key := sqlExplorerKey(t, f, "1", "v")
				var versions []Version
				for _, version := range f.store.committedVersions(key) {
					if version.ID != 0 {
						versions = append(versions, version.ID)
					}
				}
				if len(versions) != 2 {
					t.Fatalf("expected two historical versions: %+v", versions)
				}
				recorder.force[key] = ReadSource{Version: versions[0]}
				f.query(t, "BEGIN")
				namedSavepointRows(t, f, "SELECT v,n FROM items WHERE id=1", []sql.Row{{int32(11), nil}})
				f.query(t, "COMMIT")
				foundOld, foundRoot := false, false
				for _, admission := range f.store.Admissions() {
					foundOld = foundOld || admission.Key == key && admission.Selected == versions[0]
					foundRoot = foundRoot || admission.Key == sqlExplorerKey(t, f, "1", "n") && admission.Initial
				}
				if !foundOld || !foundRoot {
					t.Fatal("mixed historical value/root NULL provenance missing")
				}
				delete(recorder.force, key)
				reader := sqlExplorerPeer(t, f, 3)
				reader.query(t, "SET dbcop_read_latest=0")
				reader.query(t, "BEGIN")
				rows := reader.query(t, "SELECT v,n FROM items WHERE id=1")
				if len(rows) != 1 || len(rows[0]) != 2 || rows[0][1] != nil || (rows[0][0] != int32(10) && rows[0][0] != int32(11) && rows[0][0] != int32(12)) {
					t.Fatalf("ordinary solver choice returned an unsupported SQL outcome: %v", rows)
				}
				reader.query(t, "COMMIT")
				sqlExplorerProvenance(t, f, recorder)
				observations = append(observations, sqlExplorerValidate(t, recorder)...)
			})
			t.Run("full-commit-admission", func(t *testing.T) {
				proxy := newSQLRPCProxy(t, url)
				f, recorder := newSQLExplorerFixture(t, iso, proxy.server.URL)
				second := sqlExplorerPeer(t, f, 2)
				recorder.force[sqlExplorerKey(t, f, "1", "v")] = ReadSource{Initial: true}
				for i, peer := range []*sqlTableFixture{f, second} {
					peer.query(t, "BEGIN")
					namedSavepointRows(t, peer, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(10)}})
					peer.query(t, fmt.Sprintf("UPDATE items SET v=%d WHERE id=1", 11+i))
					if i == 1 {
						peer.query(t, "INSERT INTO items VALUES (3,30,300)")
					}
					before := f.store.CommittedHistory()
					err := foreignKeyQueryError(peer, "COMMIT")
					reject := i == 1 && iso >= SnapshotIsolation
					if reject {
						if !sql.ErrLockDeadlock.Is(err) {
							t.Fatalf("disabled commit did not return isolation rejection: %v", err)
						}
						if !reflect.DeepEqual(before, f.store.CommittedHistory()) {
							t.Fatal("disabled commit published history")
						}
						proposed := recorder.requests[len(recorder.requests)-1].Transaction
						for _, write := range proposed.Writes {
							for _, version := range f.store.committedVersions(write.Key) {
								if version.ID == write.ID {
									t.Fatalf("rejected version entered the read universe: %+v", write)
								}
							}
						}
						assertSessionTransactionEnded(t, peer.ctx.Session.(*Session))
					} else if err != nil {
						t.Fatal(err)
					}
				}
				delete(recorder.force, sqlExplorerKey(t, f, "1", "v"))
				reader := sqlExplorerPeer(t, f, 3)
				want := int32(12)
				if iso >= SnapshotIsolation {
					want = 11
				}
				namedSavepointRows(t, reader, "SELECT v FROM items WHERE id=1", []sql.Row{{want}})
				if iso >= SnapshotIsolation {
					namedSavepointRows(t, reader, "SELECT v FROM items WHERE id=3", nil)
				}
				reader.query(t, "BEGIN")
				reader.query(t, "UPDATE items SET n=201 WHERE id=2")
				reader.query(t, "COMMIT")
				sqlExplorerProvenance(t, f, recorder)
				observations = append(observations, sqlExplorerValidate(t, recorder)...)
			})
		})
	}
	verifyTLCObservations(t, observations)
}

// The proxy always forwards positive semantics to the real service. Faults only
// damage the transport/protocol boundary, and disposeSpec is never faulted.
type sqlRPCProxy struct {
	server   *httptest.Server
	target   string
	mu       sync.Mutex
	fault    string
	cancel   context.CancelFunc
	entered  chan struct{}
	release  chan struct{}
	loaded   map[string]bool
	disposed map[string]bool
	lost     map[string]bool
}

func newSQLRPCProxy(t *testing.T, target string) *sqlRPCProxy {
	t.Helper()
	p := &sqlRPCProxy{target: target, loaded: make(map[string]bool), disposed: make(map[string]bool), lost: make(map[string]bool)}
	p.server = httptest.NewServer(http.HandlerFunc(p.serveHTTP))
	t.Cleanup(func() {
		p.recoverLost(t)
		p.assertDisposed(t)
		p.server.Close()
	})
	return p
}

func (p *sqlRPCProxy) arm(fault string, cancel context.CancelFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fault, p.cancel = fault, cancel
}

func (p *sqlRPCProxy) serveHTTP(w http.ResponseWriter, incoming *http.Request) {
	body, err := io.ReadAll(incoming.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var request struct {
		Method string `json:"method"`
		Params struct {
			Session string `json:"sessionId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	outgoing, err := http.NewRequestWithContext(incoming.Context(), http.MethodPost, p.target, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	outgoing.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(outgoing)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(data, &envelope)
	var result map[string]json.RawMessage
	_ = json.Unmarshal(envelope["result"], &result)
	var session string
	_ = json.Unmarshal(result["sessionId"], &session)
	p.mu.Lock()
	if response.StatusCode == http.StatusOK && envelope["error"] == nil {
		if request.Method == "loadSpec" && session != "" {
			p.loaded[session] = true
		}
		if request.Method == "disposeSpec" && session == request.Params.Session {
			p.disposed[session] = true
		}
	}
	fault, cancel := p.fault, p.cancel
	match := fault == "unknown" && request.Method == "assumeTransition" || fault == "lost-load" && request.Method == "loadSpec" || fault == "cancel-dispose" && request.Method == "disposeSpec" || (fault == "http" || fault == "identity" || fault == "itf" || fault == "cancel-query" || fault == "hold-query" || fault == "timeout-query") && request.Method == "query"
	if match {
		p.fault = ""
	}
	if match && fault == "lost-load" && session != "" {
		p.lost[session] = true
	}
	entered, release := p.entered, p.release
	p.mu.Unlock()
	if match {
		switch fault {
		case "unknown":
			result["status"] = json.RawMessage(`"UNKNOWN"`)
		case "identity":
			result["sessionId"] = json.RawMessage(`"wrong-session"`)
		case "itf":
			result["operatorValue"] = json.RawMessage(`{}`)
		case "http", "lost-load":
			http.Error(w, "injected real-RPC response loss", http.StatusServiceUnavailable)
			return
		case "cancel-query", "cancel-dispose":
			cancel()
		case "timeout-query":
			<-incoming.Context().Done()
			return
		case "hold-query":
			close(entered)
			select {
			case <-release:
			case <-incoming.Context().Done():
				return
			}
		}
		if fault == "unknown" || fault == "identity" || fault == "itf" {
			envelope["result"], _ = json.Marshal(result)
			data, _ = json.Marshal(envelope)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(data)
}

func (p *sqlRPCProxy) recoverLost(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	var lost []string
	for session := range p.lost {
		if !p.disposed[session] {
			lost = append(lost, session)
		}
	}
	p.mu.Unlock()
	if len(lost) == 0 {
		return
	}
	client, err := apalachebackend.New(p.server.URL, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, session := range lost {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var result struct {
			Session string `json:"sessionId"`
		}
		err := client.Call(ctx, "disposeSpec", map[string]any{"sessionId": session}, &result)
		cancel()
		if err != nil || result.Session != session {
			t.Fatalf("test-owned lost session cleanup %q: %v", session, err)
		}
	}
}

func (p *sqlRPCProxy) assertDisposed(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for session := range p.loaded {
		if !p.disposed[session] {
			t.Errorf("real RPC session %s was not disposed", session)
		}
	}
	if p.fault != "" {
		t.Errorf("armed fault %q was not exercised", p.fault)
	}
}

func sqlExplorerArm(recorder *sqlExplorerRecorder, proxy *sqlRPCProxy, kind, key string, nth int, fault string) *int {
	count := new(int)
	recorder.before = func(ctx context.Context, request StepRequest) (context.Context, func()) {
		if request.Kind != kind || key != "" && request.Key != key {
			return ctx, func() {}
		}
		(*count)++
		if *count != nth {
			return ctx, func() {}
		}
		var cancel context.CancelFunc
		if fault == "timeout-query" {
			ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		} else {
			ctx, cancel = context.WithCancel(ctx)
		}
		proxy.arm(fault, cancel)
		return ctx, cancel
	}
	return count
}

func sqlExplorerInfrastructureError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || sql.ErrLockDeadlock.Is(err) || errors.Is(err, errCommitRejected) || !strings.Contains(err.Error(), contains) {
		t.Fatalf("expected infrastructure SQL error containing %q, not absence/rejection: %v", contains, err)
	}
}

func TestApalacheFailureBoundaries(t *testing.T) {
	url := apalacheTestURL(t)
	var observations []tlcObservation
	for _, test := range []struct {
		name, kind, query, fault, cause string
		nth                             int
	}{
		{"read-unknown", "read", "SELECT n FROM items WHERE id=1", "unknown", "UNKNOWN", 1},
		{"null-column-unknown", "read", "SELECT n FROM items WHERE id=1", "unknown", "UNKNOWN", 1},
		{"read-malformed-itf", "read", "SELECT n FROM items WHERE id=1", "itf", "Apalache Result", 1},
		{"read-wrong-session", "read", "SELECT n FROM items WHERE id=1", "identity", "query session", 1},
		{"read-http", "read", "SELECT n FROM items WHERE id=1", "http", "503", 1},
		{"read-cancel-query", "read", "SELECT n FROM items WHERE id=1", "cancel-query", "canceled", 1},
		{"read-timeout", "read", "SELECT n FROM items WHERE id=1", "timeout-query", "deadline exceeded", 1},
		{"read-cancel-successful-disposal", "read", "SELECT n FROM items WHERE id=1", "cancel-dispose", "canceled", 1},
		{"write-cancel-successful-disposal", "write", "INSERT INTO items VALUES (3,30,300)", "cancel-dispose", "canceled", 1},
		{"partial-statement", "write", "INSERT INTO items VALUES (3,30,300)", "unknown", "UNKNOWN", 2},
		{"partial-truncate", "write", "TRUNCATE TABLE items", "http", "503", 2},
		{"commit-unknown", "commit", "COMMIT", "unknown", "UNKNOWN", 1},
		{"lost-load-response", "read", "SELECT n FROM items WHERE id=1", "lost-load", "unknown remote session", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := newSQLRPCProxy(t, url)
			f, recorder := newSQLExplorerFixture(t, Serializable, proxy.server.URL)
			f.query(t, "BEGIN")
			if test.kind == "commit" {
				f.query(t, "UPDATE items SET v=11 WHERE id=1")
			}
			before := f.store.CommittedHistory()
			key := ""
			if test.name == "null-column-unknown" {
				key = sqlExplorerKey(t, f, "1", "n")
			}
			count := sqlExplorerArm(recorder, proxy, test.kind, key, test.nth, test.fault)
			err := foreignKeyQueryError(f, test.query)
			sqlExplorerInfrastructureError(t, err, test.cause)
			if *count < test.nth {
				t.Fatal("fault did not follow the required successful prefix")
			}
			if test.nth > 1 {
				succeeded := 0
				for i, request := range recorder.requests {
					if request.Kind == test.kind && recorder.results[i].Enabled {
						succeeded++
					}
				}
				if succeeded == 0 {
					t.Fatal("partial-write fault did not follow a real successful stage")
				}
			}
			if !reflect.DeepEqual(before, f.store.CommittedHistory()) {
				t.Fatal("infrastructure error published history")
			}
			if test.kind == "commit" {
				assertSessionTransactionEnded(t, f.ctx.Session.(*Session))
			}
			recorder.before = nil
			proxy.recoverLost(t)
			proxy.assertDisposed(t)
			// COMMIT after a failed statement must not publish its successful partial stages.
			f.query(t, "COMMIT")
			for _, txn := range f.store.CommittedHistory() {
				if len(txn.Writes) != 0 {
					t.Fatalf("failed operation published writes: %+v", txn)
				}
			}
			namedSavepointRows(t, f, "SELECT id,v,n FROM items ORDER BY id", []sql.Row{{int32(1), int32(10), nil}, {int32(2), int32(20), int32(200)}})
			namedSavepointRows(t, f, "SELECT v FROM items WHERE id=3", nil)
			f.query(t, "BEGIN")
			f.query(t, "UPDATE items SET v=11 WHERE id=1")
			f.query(t, "COMMIT")
			namedSavepointRows(t, f, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(11)}})
			proxy.assertDisposed(t)
			sqlExplorerProvenance(t, f, recorder)
			observations = append(observations, sqlExplorerValidate(t, recorder)...)
		})
	}
	t.Run("successful-truncate-is-implicit-commit", func(t *testing.T) {
		proxy := newSQLRPCProxy(t, url)
		f, recorder := newSQLExplorerFixture(t, Serializable, proxy.server.URL)
		f.query(t, "BEGIN")
		f.query(t, "TRUNCATE TABLE items")
		f.query(t, "ROLLBACK")
		namedSavepointRows(t, f, "SELECT id,v FROM items", nil)
		if len(f.store.CommittedHistory()) == 0 {
			t.Fatal("successful TRUNCATE was not committed")
		}
		proxy.assertDisposed(t)
		sqlExplorerProvenance(t, f, recorder)
		observations = append(observations, sqlExplorerValidate(t, recorder)...)
	})
	t.Run("independent-stores-and-shutdown", func(t *testing.T) {
		firstProxy, secondProxy := newSQLRPCProxy(t, url), newSQLRPCProxy(t, url)
		first, firstRecorder := newSQLExplorerFixture(t, Serializable, firstProxy.server.URL)
		second, secondRecorder := newSQLExplorerFixture(t, Serializable, secondProxy.server.URL)
		first.query(t, "BEGIN")
		first.query(t, "UPDATE items SET v=11 WHERE id=1")
		first.ctx.Session.(*Session).SessionEnd()
		if len(first.store.CommittedHistory()) != 0 || len(second.store.CommittedHistory()) != 0 {
			t.Fatal("aborted operation crossed store publication boundary")
		}
		// Hold a real returned model in one store while the other makes progress.
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		firstProxy.mu.Lock()
		firstProxy.entered, firstProxy.release = entered, release
		firstProxy.mu.Unlock()
		firstProxy.arm("hold-query", nil)
		done := make(chan error, 1)
		go func() { done <- foreignKeyQueryError(first, "SELECT v FROM items WHERE id=1") }()
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("first store did not reach real query barrier")
		}
		second.query(t, "BEGIN")
		second.query(t, "UPDATE items SET v=12 WHERE id=1")
		second.query(t, "COMMIT")
		unblock()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("first store did not finish after releasing its query")
		}
		namedSavepointRows(t, first, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(10)}})
		namedSavepointRows(t, second, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(12)}})
		firstProxy.mu.Lock()
		secondProxy.mu.Lock()
		for session := range firstProxy.loaded {
			if secondProxy.loaded[session] {
				t.Errorf("independent stores shared real session %s", session)
			}
		}
		secondProxy.mu.Unlock()
		firstProxy.mu.Unlock()
		firstRecorder.shutdown()
		firstProxy.assertDisposed(t)
		// Closing one store must not close the other store's explorer.
		second.query(t, "BEGIN")
		second.query(t, "UPDATE items SET v=13 WHERE id=1")
		second.query(t, "COMMIT")
		namedSavepointRows(t, second, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(13)}})
		secondProxy.assertDisposed(t)
		sqlExplorerProvenance(t, first, firstRecorder)
		sqlExplorerProvenance(t, second, secondRecorder)
		observations = append(observations, sqlExplorerValidate(t, firstRecorder)...)
		observations = append(observations, sqlExplorerValidate(t, secondRecorder)...)
	})
	verifyTLCObservations(t, observations)
}
