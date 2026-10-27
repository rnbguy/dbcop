package backend

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type replicaTraceBounds struct {
	Replicas, Transactions, Events, Depth, Pattern int
}

type replicaTraceEvent struct {
	Kind    int
	Key     int
	Initial bool
	Version uint64
}

type replicaTraceHistory [][][]replicaTraceEvent

type replicaTraceStats struct {
	Generated   int
	Exhausted   bool
	Artifacts   string
	TimedOut    bool
	SafeToRetry bool
}

func (b replicaTraceBounds) validate() error {
	if b.Replicas < 1 || b.Transactions < 0 || b.Events < 0 || b.Depth < 0 || b.Depth == math.MaxInt {
		return fmt.Errorf("invalid replica trace bounds %+v", b)
	}
	if b.Pattern < 0 || b.Pattern > 3 || b.Pattern != 0 && (b.Replicas != 2 || b.Transactions != 2 || b.Depth < 8 || b.Events < 3 || b.Pattern < 3 && b.Events < 4) {
		return fmt.Errorf("invalid targeted replica trace bounds %+v", b)
	}
	return nil
}

func renderReplicaTraceData(b replicaTraceBounds) string {
	return fmt.Sprintf("---- MODULE TraceData ----\nReplicaCount == %d\nMaxTransactions == %d\nMaxEvents == %d\nMaxDepth == %d\nTargetPattern == %d\n====\n",
		b.Replicas, b.Transactions, b.Events, b.Depth, b.Pattern)
}

// Pointer fields distinguish required false/zero values from missing or null ITF fields.
type replicaTraceITFEvent struct {
	Kind    *apalacheBigInt `json:"kind"`
	Key     *apalacheBigInt `json:"key"`
	Initial *bool           `json:"initial"`
	Version *apalacheBigInt `json:"version"`
}

func decodeReplicaTraceHistory(raw json.RawMessage, b replicaTraceBounds) (replicaTraceHistory, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	var result struct {
		Sessions *[][][]*replicaTraceITFEvent `json:"sessions"`
	}
	if err := jsonv2.Unmarshal(raw, &result, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("invalid replica trace Result: %w", err)
	}
	if result.Sessions == nil || len(*result.Sessions) != b.Replicas {
		return nil, fmt.Errorf("replica trace requires exactly %d sessions", b.Replicas)
	}
	history := make(replicaTraceHistory, b.Replicas)
	transactions, events := 0, 0
	writes := make(map[uint64]bool)
	for s, session := range *result.Sessions {
		if session == nil || len(session) > b.Transactions-transactions {
			return nil, fmt.Errorf("null session or transaction bound exceeded at session %d", s)
		}
		transactions += len(session)
		history[s] = make([][]replicaTraceEvent, len(session))
		for t, txn := range session {
			if txn == nil || len(txn) > b.Events-events {
				return nil, fmt.Errorf("null transaction or event bound exceeded at %d/%d", s, t)
			}
			events += len(txn)
			history[s][t] = make([]replicaTraceEvent, len(txn))
			for p, event := range txn {
				if event == nil || event.Kind == nil || event.Key == nil || event.Initial == nil || event.Version == nil {
					return nil, fmt.Errorf("missing replica event field at %d/%d/%d", s, t, p)
				}
				kind, key, version := uint64(*event.Kind), uint64(*event.Key), uint64(*event.Version)
				if kind > 1 || key > 1 || version > uint64(b.Events) || *event.Initial != (version == 0) || kind == 1 && *event.Initial {
					return nil, fmt.Errorf("invalid replica event at %d/%d/%d", s, t, p)
				}
				if kind == 1 {
					if writes[version] {
						return nil, fmt.Errorf("duplicate global write version %d", version)
					}
					writes[version] = true
				}
				history[s][t][p] = replicaTraceEvent{Kind: int(kind), Key: int(key), Initial: *event.Initial, Version: version}
			}
		}
	}
	// Every completed transaction needs Begin and Commit, in addition to its events.
	if transactions > b.Depth/2 || events > b.Depth-2*transactions {
		return nil, fmt.Errorf("replica history exceeds transition depth %d", b.Depth)
	}
	return history, nil
}

type replicaTraceNextModel struct {
	Session string          `json:"sessionId"`
	Old     json.RawMessage `json:"oldValue"`
	HasOld  string          `json:"hasOld"`
	HasNext string          `json:"hasNext"`
}

func (r replicaTraceNextModel) decode(session string, b replicaTraceBounds) (replicaTraceHistory, bool, error) {
	if !validApalacheSession(session) || r.Session != session || r.HasOld != "TRUE" {
		return nil, false, fmt.Errorf("inconclusive replica nextModel session/hasOld: session=%q hasOld=%q", r.Session, r.HasOld)
	}
	if r.HasNext != "TRUE" && r.HasNext != "FALSE" {
		return nil, false, fmt.Errorf("inconclusive replica nextModel hasNext %q", r.HasNext)
	}
	history, err := decodeReplicaTraceHistory(r.Old, b)
	return history, r.HasNext == "FALSE" && err == nil, err
}

// The directory deliberately survives testing.T cleanup, including timeouts and callback failures.
func enumerateReplicaTraceHistories(ctx context.Context, e *apalacheExplorer, b replicaTraceBounds, emit func(replicaTraceHistory) error) (stats replicaTraceStats, err error) {
	if err = b.validate(); err != nil {
		return stats, err
	}
	if e == nil || e.client == nil || emit == nil || ctx == nil {
		return stats, fmt.Errorf("replica enumeration requires context, explorer, and callback")
	}
	stats.Artifacts, err = os.MkdirTemp("", "dbcop-apalache-traces-")
	if err != nil {
		return stats, err
	}
	defer func() {
		if err != nil {
			stats.Exhausted = false
		}
		summary := struct {
			Bounds replicaTraceBounds
			Stats  replicaTraceStats
			Error  string
		}{Bounds: b, Stats: stats}
		if err != nil {
			summary.Error = err.Error()
		}
		data, marshalErr := json.MarshalIndent(summary, "", "  ")
		if marshalErr == nil {
			marshalErr = os.WriteFile(filepath.Join(stats.Artifacts, "result.json"), data, 0600)
		}
		err = errors.Join(err, marshalErr)
		if err != nil {
			stats.Exhausted = false
			err = fmt.Errorf("replica traces (artifacts %s): %w", stats.Artifacts, err)
		}
	}()
	logFile, err := os.OpenFile(filepath.Join(stats.Artifacts, "rpc.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return stats, err
	}
	defer func() { err = errors.Join(err, logFile.Close()) }()
	historyFile, err := os.OpenFile(filepath.Join(stats.Artifacts, "histories.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return stats, err
	}
	defer func() { err = errors.Join(err, historyFile.Close()) }()
	logEncoder, historyEncoder := json.NewEncoder(logFile), json.NewEncoder(historyFile)
	logRPC := func(method string, response any, callErr error) error {
		entry := struct {
			Time     time.Time `json:"time"`
			Method   string    `json:"method"`
			Response any       `json:"response"`
			Error    string    `json:"error,omitempty"`
		}{Time: time.Now().UTC(), Method: method, Response: response}
		if callErr != nil {
			entry.Error = callErr.Error()
		}
		return errors.Join(callErr, logEncoder.Encode(entry))
	}
	call := func(callCtx context.Context, method string, params any, response any) error {
		return logRPC(method, response, e.client.Call(callCtx, method, params, response))
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return stats, fmt.Errorf("Apalache explorer is closed")
	}
	e.active.Add(1)
	e.mu.Unlock()
	defer e.active.Done()

	// One shared budget covers source rendering, load, Init, every unrolled step, and query.
	generation, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	timedOut := false
	defer func() { stats.TimedOut = timedOut }()
	noteTimeout := func(attempt context.Context, attemptErr error) error {
		if errors.Is(attemptErr, context.DeadlineExceeded) {
			timedOut = true
		}
		if attempt.Err() != nil {
			if errors.Is(attempt.Err(), context.DeadlineExceeded) {
				timedOut = true
			}
			return errors.Join(attemptErr, attempt.Err())
		}
		return attemptErr
	}
	var load apalacheLoaded
	unknownAllocation := false
	defer func() {
		if load.Session == "" {
			stats.SafeToRetry = !unknownAllocation
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		var disposed apalacheSession
		cleanupErr := call(cleanup, "disposeSpec", apalacheSession{load.Session}, &disposed)
		if cleanupErr == nil && disposed.Session != load.Session {
			cleanupErr = fmt.Errorf("invalid replica disposal session %q", disposed.Session)
		}
		stats.SafeToRetry = cleanupErr == nil && !unknownAllocation && validApalacheSession(load.Session)
		err = errors.Join(err, cleanupErr)
	}()
	model, err := os.ReadFile(filepath.Join("..", "..", "crates", "core", "tests", "tla", "ReplicaTraces.tla"))
	if err != nil {
		return stats, noteTimeout(generation, err)
	}
	data := renderReplicaTraceData(b)
	for name, contents := range map[string][]byte{"ReplicaTraces.tla": model, "TraceData.tla": []byte(data)} {
		if err = os.WriteFile(filepath.Join(stats.Artifacts, name), contents, 0600); err != nil {
			return stats, noteTimeout(generation, err)
		}
	}
	metadata, err := json.Marshal(struct {
		ModelSHA256         string
		DataSHA256          string
		ExpectedTool        string
		TraceTimeoutSeconds int
		SMTSeed             string
	}{
		ModelSHA256:         fmt.Sprintf("%x", sha256.Sum256(model)),
		DataSHA256:          fmt.Sprintf("%x", sha256.Sum256([]byte(data))),
		ExpectedTool:        "externally-provisioned Apalache v0.62.4; RPC does not attest version",
		TraceTimeoutSeconds: 10,
		SMTSeed:             "external/not-configured",
	})
	if err != nil {
		return stats, noteTimeout(generation, err)
	}
	if err = os.WriteFile(filepath.Join(stats.Artifacts, "metadata.json"), metadata, 0600); err != nil {
		return stats, noteTimeout(generation, err)
	}
	if err = generation.Err(); err != nil {
		return stats, noteTimeout(generation, err)
	}
	params := struct {
		Sources []string `json:"sources"`
		Format  string   `json:"format"`
		Init    string   `json:"init"`
		Next    string   `json:"next"`
		Exports []string `json:"exports"`
	}{[]string{base64.StdEncoding.EncodeToString(model), base64.StdEncoding.EncodeToString([]byte(data))}, "tla", "Init", "Next", []string{"Result"}}
	if err = call(generation, "loadSpec", params, &load); err != nil {
		unknownAllocation = load.Session == ""
		return stats, noteTimeout(generation, fmt.Errorf("loadSpec failed; a lost reply can leave an unrecoverable unknown session: %w", err))
	}
	unknownAllocation = load.Session == ""
	init, next, err := apalacheTransitions(load)
	if err != nil {
		return stats, noteTimeout(generation, err)
	}
	steps := b.Depth
	if b.Pattern != 0 {
		steps -= b.Replicas // The model includes the commuting Begin prefix in Init.
	}
	for step := 0; step <= steps; step++ {
		transition := next
		if step == 0 {
			transition = init
		}
		enabled, assumeErr := e.assume(generation, load.Session, transition)
		if err = logRPC("assumeTransition", struct {
			Session    string
			Step       int
			Transition int
			Enabled    bool
		}{load.Session, step, transition, enabled}, assumeErr); err != nil {
			return stats, noteTimeout(generation, err)
		}
		if !enabled {
			return stats, noteTimeout(generation, fmt.Errorf("replica transition disabled at unroll step %d", step))
		}
		snapshot, advanceErr := e.advance(generation, load.Session, step+1)
		if err = logRPC("nextStep", struct {
			Session  string
			Step     int
			Snapshot int
		}{load.Session, step + 1, snapshot}, advanceErr); err != nil {
			return stats, noteTimeout(generation, err)
		}
	}
	var query struct {
		Session string          `json:"sessionId"`
		Value   json.RawMessage `json:"operatorValue"`
	}
	if err = call(generation, "query", struct {
		Session  string   `json:"sessionId"`
		Kinds    []string `json:"kinds"`
		Operator string   `json:"operator"`
		Timeout  int      `json:"timeoutSec"`
	}{load.Session, []string{"OPERATOR"}, "Result", e.solverTimeout(generation)}, &query); err != nil {
		return stats, noteTimeout(generation, err)
	}
	if query.Session != load.Session {
		return stats, noteTimeout(generation, fmt.Errorf("invalid replica query session"))
	}
	first, err := decodeReplicaTraceHistory(query.Value, b)
	if err = noteTimeout(generation, err); err != nil {
		return stats, err
	}
	cancel() // The potentially expensive comparison callback is not generation work.
	seen := make(map[string]bool)
	publish := func(history replicaTraceHistory) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		encoded, err := json.Marshal(history)
		if err != nil {
			return "", err
		}
		key := string(encoded)
		if seen[key] {
			return "", fmt.Errorf("duplicate projected replica history")
		}
		if stats.Generated == math.MaxInt {
			return "", fmt.Errorf("replica history count overflow")
		}
		if err := historyEncoder.Encode(history); err != nil {
			return "", err
		}
		seen[key] = true
		stats.Generated++
		if err := emit(history); err != nil {
			return "", err
		}
		return key, ctx.Err()
	}
	if _, err = publish(first); err != nil {
		return stats, err
	}
	blocked := make(map[string]bool)
	for {
		attempt, stop := context.WithTimeout(ctx, 10*time.Second)
		var response replicaTraceNextModel
		callErr := call(attempt, "nextModel", struct {
			Session  string `json:"sessionId"`
			Operator string `json:"operator"`
			Timeout  int    `json:"timeoutSec"`
		}{load.Session, "Result", e.solverTimeout(attempt)}, &response)
		var history replicaTraceHistory
		var exhausted bool
		if callErr == nil {
			history, exhausted, callErr = response.decode(load.Session, b)
		}
		callErr = noteTimeout(attempt, callErr)
		stop()
		if callErr != nil {
			return stats, callErr
		}
		// query does not pin a model: its projection may reappear at any enumeration step.
		encoded, encodeErr := json.Marshal(history)
		if encodeErr != nil {
			return stats, encodeErr
		}
		key := string(encoded)
		if blocked[key] {
			return stats, fmt.Errorf("duplicate nextModel oldValue projection")
		}
		blocked[key] = true
		if !seen[key] {
			_, err = publish(history)
		}
		if err != nil {
			return stats, err
		}
		if err = ctx.Err(); err != nil {
			return stats, err
		}
		if exhausted {
			stats.Exhausted = true
			return stats, nil
		}
	}
}

func TestReplicaTraceDecoder(t *testing.T) {
	b := replicaTraceBounds{Replicas: 2, Transactions: 1, Events: 1, Depth: 3}
	valid := `{"sessions":[[[{"kind":{"#bigint":"0"},"key":{"#bigint":"1"},"initial":true,"version":{"#bigint":"0"}}]],[]]}`
	if history, err := decodeReplicaTraceHistory(json.RawMessage(valid), b); err != nil || len(history) != 2 || len(history[0]) != 1 || history[0][0][0] != (replicaTraceEvent{Key: 1, Initial: true}) {
		t.Fatalf("valid history=%+v error=%v", history, err)
	}
	for _, raw := range []string{
		`{}`, `{"sessions":null}`, `{"sessions":[null,[]]}`, `{"sessions":[[null],[]]}`,
		`{"sessions":[[[null]],[]]}`,
		`{"sessions":[[[{"kind":{"#bigint":"0"},"key":{"#bigint":"0"},"version":{"#bigint":"0"}}]],[]]}`,
		`{"sessions":[[[{"kind":{"#bigint":"-1"},"key":{"#bigint":"0"},"initial":true,"version":{"#bigint":"0"}}]],[]]}`,
		`{"sessions":[[[{"kind":{"#bigint":"2"},"key":{"#bigint":"0"},"initial":true,"version":{"#bigint":"0"}}]],[]]}`,
		`{"sessions":[[[{"kind":{"#bigint":"0"},"key":{"#bigint":"2"},"initial":true,"version":{"#bigint":"0"}}]],[]]}`,
		`{"sessions":[[[{"kind":{"#bigint":"0"},"key":{"#bigint":"0"},"initial":false,"version":{"#bigint":"18446744073709551616"}}]],[]]}`,
		`{"sessions":[[[{"kind":{"#bigint":"1"},"key":{"#bigint":"0"},"initial":true,"version":{"#bigint":"0"}}]],[]]}`,
		`{"sessions":[[[{"kind":{"#bigint":"0"},"key":{"#bigint":"0"},"initial":false,"version":{"#bigint":"2"}}]],[]]}`,
		`{"sessions":[[],[]],"extra":0}`,
	} {
		if _, err := decodeReplicaTraceHistory(json.RawMessage(raw), b); err == nil {
			t.Errorf("accepted malformed or unbounded Result %s", raw)
		}
	}
	for _, limited := range []replicaTraceBounds{
		{Replicas: 1, Transactions: 1, Events: 1, Depth: 3},
		{Replicas: 2, Transactions: 0, Events: 1, Depth: 3},
		{Replicas: 2, Transactions: 1, Events: 0, Depth: 3},
		{Replicas: 2, Transactions: 1, Events: 1, Depth: 2},
		{Replicas: -1}, {Replicas: 1, Events: -1}, {Replicas: 1, Depth: math.MaxInt},
	} {
		if _, err := decodeReplicaTraceHistory(json.RawMessage(valid), limited); err == nil {
			t.Errorf("accepted exceeded bounds %+v", limited)
		}
	}
}

func TestReplicaTraceNextModelProtocol(t *testing.T) {
	b := replicaTraceBounds{Replicas: 1}
	for _, fixture := range []struct {
		session, old, next   string
		wantError, exhausted bool
	}{
		{"abc", "TRUE", "FALSE", false, true},
		{"abc", "TRUE", "TRUE", false, false},
		{"abc", "FALSE", "FALSE", true, false},
		{"abc", "UNKNOWN", "FALSE", true, false},
		{"abc", "TRUE", "UNKNOWN", true, false},
		{"abc", "", "FALSE", true, false},
		{"def", "TRUE", "FALSE", true, false},
		{"", "TRUE", "FALSE", true, false},
	} {
		response := replicaTraceNextModel{Session: fixture.session, Old: json.RawMessage(`{"sessions":[[]]}`), HasOld: fixture.old, HasNext: fixture.next}
		_, exhausted, err := response.decode("abc", b)
		if (err != nil) != fixture.wantError || exhausted != fixture.exhausted {
			t.Errorf("response=%+v exhausted=%v error=%v", response, exhausted, err)
		}
	}
}
