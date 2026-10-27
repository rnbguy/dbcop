//go:build cgo

package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rnbguy/dbcop/mockserver/apalachebackend"
)

type replicaTraceTxn struct {
	session int
	height  int
}

type replicaTraceWriter struct {
	txn   replicaTraceTxn
	key   int
	final bool
}

// Validate primitive identities before using a generated history as checker input.
func replicaTraceSessions(history replicaTraceHistory) ([][]historyTxn, []replicaTraceTxn, error) {
	sessions := make([][]historyTxn, len(history))
	writers := make(map[uint64]replicaTraceWriter)
	for s, session := range history {
		sessions[s] = make([]historyTxn, len(session))
		for h, events := range session {
			last := make(map[int]uint64)
			for _, event := range events {
				if event.Kind == 1 {
					if event.Initial || event.Version == 0 {
						return nil, nil, fmt.Errorf("invalid generated write: %+v", event)
					}
					if _, exists := writers[event.Version]; exists {
						return nil, nil, fmt.Errorf("duplicate generated write version %d", event.Version)
					}
					writers[event.Version] = replicaTraceWriter{txn: replicaTraceTxn{s, h}, key: event.Key}
					last[event.Key] = event.Version
				}
			}
			for _, version := range last {
				writer := writers[version]
				writer.final = true
				writers[version] = writer
			}
		}
	}
	dependencies := make(map[replicaTraceTxn][]replicaTraceTxn)
	for s, session := range history {
		for h, events := range session {
			txn := historyTxn{Events: make([]historyEvent, 0, len(events)), Committed: true}
			local := make(map[int]uint64)
			for _, event := range events {
				if event.Key < 0 || event.Key > 1 || event.Kind < 0 || event.Kind > 1 {
					return nil, nil, fmt.Errorf("invalid generated event: %+v", event)
				}
				version := event.Version
				cell := &historyRW{Variable: uint64(event.Key), Version: &version}
				if event.Kind == 1 {
					local[event.Key] = version
					txn.Events = append(txn.Events, historyEvent{Write: cell})
					continue
				}
				if preceding, exists := local[event.Key]; exists {
					if event.Initial || version != preceding {
						return nil, nil, fmt.Errorf("generated local read does not match latest write: %+v", event)
					}
				} else if event.Initial {
					if version != 0 {
						return nil, nil, fmt.Errorf("initial read has nonzero version %d", version)
					}
				} else {
					writer, exists := writers[version]
					if !exists || writer.key != event.Key || !writer.final || writer.txn == (replicaTraceTxn{s, h}) {
						return nil, nil, fmt.Errorf("generated read has no final external writer: %+v", event)
					}
					dependencies[replicaTraceTxn{s, h}] = append(dependencies[replicaTraceTxn{s, h}], writer.txn)
				}
				if event.Initial {
					cell.Version = nil
				}
				txn.Events = append(txn.Events, historyEvent{Read: cell})
			}
			sessions[s][h] = txn
		}
	}
	placed := make(map[replicaTraceTxn]bool)
	heights := make([]int, len(sessions))
	var arrival []replicaTraceTxn
	for {
		remaining, progressed := false, false
		for s, session := range sessions {
			if heights[s] == len(session) {
				continue
			}
			remaining = true
			next := replicaTraceTxn{s, heights[s]}
			ready := true
			for _, source := range dependencies[next] {
				ready = ready && placed[source]
			}
			if ready {
				placed[next] = true
				heights[s]++
				arrival = append(arrival, next)
				progressed = true
			}
		}
		if !remaining {
			return sessions, arrival, nil
		}
		if !progressed {
			return nil, nil, fmt.Errorf("generated history has no source-available arrival order")
		}
	}
}

func compareReplicaTraceIncremental(t *testing.T, sessions [][]historyTxn, arrival []replicaTraceTxn, record func([][]historyTxn, Isolation, bool)) {
	t.Helper()
	for level := ReadCommitted; level <= Serializable; level++ {
		driver := newNativeDriver(level)
		func() {
			defer driver.close()
			prefix := make([][]historyTxn, len(sessions))
			for s := range prefix {
				prefix[s] = []historyTxn{}
			}
			for _, position := range arrival {
				events := sessions[position.session][position.height].Events
				candidate := slices.Clone(prefix)
				candidate[position.session] = append(slices.Clone(prefix[position.session]), sessions[position.session][position.height])
				expected := checkLevel(candidate, levelName(level))
				record(candidate, level, expected)
				trial := driver.check(position.session+1, events, false)
				driver.reset()
				afterReset := driver.check(position.session+1, events, false)
				committed := driver.check(position.session+1, events, true)
				if trial != expected || afterReset != expected || committed != expected {
					t.Fatalf("incremental mismatch level=%s trial=%t reset=%t commit=%t expected=%t history=%+v", levelName(level), trial, afterReset, committed, expected, candidate)
				}
				if !expected {
					return
				}
				prefix = candidate
			}
		}()
	}
}

func verifyReplicaTraceCases(t *testing.T, cases [][2]any) {
	t.Helper()
	if len(cases) == 0 || os.Getenv("DBCOP_TLC_JAR") == "" {
		t.Fatal("generated histories and DBCOP_TLC_JAR are required")
	}
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "apalache_histories.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "cargo", "test", "--release", "-p", "dbcop_core", "--features", "serde", "--test", "tlc_model", "tlc_go_histories", "--", "--exact", "--ignored", "--nocapture")
	command.Dir = filepath.Join("..", "..")
	command.Env = append(os.Environ(), "DBCOP_GO_HISTORIES="+path)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generated Apalache/TLC/core/reference comparison: %v\n%s", err, output)
	}
	marker := fmt.Sprintf("DBCOP_GO_TLC_VERIFIED|%d\n", len(cases))
	if !strings.Contains(string(output), marker) {
		t.Fatalf("missing complete comparison marker %q\n%s", marker, output)
	}
	t.Logf("TLC/core/reference/witness verified %d distinct generated histories and prefixes", len(cases))
}

func TestApalacheReplicaTraceCampaign(t *testing.T) {
	e := apalacheTestExplorer(t)
	if os.Getenv("DBCOP_TLC_JAR") == "" {
		t.Fatal("DBCOP_TLC_JAR is required for the generated-trace campaign")
	}
	profiles := []replicaTraceBounds{
		{Replicas: 1, Transactions: 1, Events: 1, Depth: 3},
		{Replicas: 2, Transactions: 1, Events: 2, Depth: 4},
		{Replicas: 3, Transactions: 1, Events: 2, Depth: 4},
		{Replicas: 2, Transactions: 2, Events: 4, Depth: 8, Pattern: 1},
		{Replicas: 2, Transactions: 2, Events: 4, Depth: 8, Pattern: 2},
		{Replicas: 2, Transactions: 2, Events: 3, Depth: 8, Pattern: 3},
		{Replicas: 1, Transactions: 2, Events: 2, Depth: 6},
		{Replicas: 2, Transactions: 2, Events: 2, Depth: 6},
	}
	indices := make(map[string]int)
	var cases [][2]any
	verified := 0
	record := func(sessions [][]historyTxn, level Isolation, expected bool) {
		encoded, err := json.Marshal(sessions)
		if err != nil {
			t.Fatal(err)
		}
		index, exists := indices[string(encoded)]
		if !exists {
			index = len(cases)
			indices[string(encoded)] = index
			cases = append(cases, [2]any{sessions, [7]*bool{}})
		}
		expectations := cases[index][1].([7]*bool)
		if prior := expectations[level]; prior != nil && *prior != expected {
			t.Fatal("contradictory native verdicts for identical generated history")
		}
		expectations[level] = &expected
		cases[index][1] = expectations
	}
	for index, requested := range profiles {
		bounds := requested
		for {
			t.Logf("campaign progress=%d/%d cells (%d%%), requested=%+v actual=%+v, per-trace-timeout=10s, solver-seed=external/not-configured", index, len(profiles), 100*index/len(profiles), requested, bounds)
			observed := make(map[string]bool)
			classifications := make(map[uint8]int)
			stats, err := enumerateReplicaTraceHistories(t.Context(), e, bounds, func(history replicaTraceHistory) error {
				encoded, err := json.Marshal(history)
				if err != nil {
					return err
				}
				observed[string(encoded)] = true
				sessions, arrival, err := replicaTraceSessions(history)
				if err != nil {
					return err
				}
				var classification uint8
				for level := ReadCommitted; level <= Serializable; level++ {
					expected := checkLevel(sessions, levelName(level))
					record(sessions, level, expected)
					if expected {
						classification |= 1 << uint8(level)
					}
				}
				classifications[classification]++
				compareReplicaTraceIncremental(t, sessions, arrival, record)
				if len(cases)-verified >= 256 {
					verifyReplicaTraceCases(t, cases[verified:])
					verified = len(cases)
				}
				if len(observed)%32 == 0 {
					t.Logf("campaign progress=%d/%d cells (%d%%), actual=%+v unique-histories=%d classification-masks=%v", index, len(profiles), 100*index/len(profiles), bounds, len(observed), classifications)
				}
				return nil
			})
			t.Logf("campaign requested=%+v actual=%+v generated=%d exhausted=%t classification-masks=%v artifacts=%s error=%v", requested, bounds, stats.Generated, stats.Exhausted, classifications, stats.Artifacts, err)
			if len(cases) > verified {
				verifyReplicaTraceCases(t, cases[verified:])
				verified = len(cases)
			}
			if err == nil {
				if !stats.Exhausted || stats.Generated == 0 {
					t.Fatal("generation returned without a complete nonempty finite enumeration")
				}
				if (bounds.Transactions <= 1 || bounds.Pattern != 0) && !maps.Equal(observed, referenceReplicaTraceHistories(t, bounds)) {
					t.Fatalf("enumeration differs from independently constructed complete domain: bounds=%+v actual=%v", bounds, observed)
				}
				if bounds.Pattern != 0 {
					expected := map[int]uint8{1: 63, 2: 31, 3: 1}[bounds.Pattern]
					if len(classifications) != 1 || classifications[expected] != stats.Generated {
						t.Fatalf("targeted workload has wrong seven-level classification: pattern=%d masks=%v", bounds.Pattern, classifications)
					}
				}
				break
			}
			if !stats.TimedOut || !stats.SafeToRetry || bounds.Depth == 0 || bounds.Pattern != 0 {
				t.Fatalf("inconclusive trace campaign: %v (artifacts %s)", err, stats.Artifacts)
			}
			bounds.Depth--
			bounds.Transactions = min(bounds.Transactions, bounds.Depth/2)
			bounds.Events = min(bounds.Events, max(0, bounds.Depth-2*bounds.Transactions))
			t.Logf("previous bound INCONCLUSIVE; reducing history bound to %+v, not claiming original bound exhausted", bounds)
		}
	}
	t.Logf("campaign progress=%d/%d cells (100%%), distinct-classified-histories-and-prefixes=%d; adapted bounds are reported separately from original inconclusive bounds", len(profiles), len(profiles), len(cases))
}

func TestReplicaTraceHistoryValidation(t *testing.T) {
	history := replicaTraceHistory{
		{{{Kind: 0, Key: 0, Version: 1}}},
		{{{Kind: 1, Key: 0, Version: 1}}},
	}
	sessions, arrival, err := replicaTraceSessions(history)
	if err != nil || !slices.Equal(arrival, []replicaTraceTxn{{1, 0}, {0, 0}}) {
		t.Fatalf("source-available arrival=%v error=%v", arrival, err)
	}
	if version := sessions[0][0].Events[0].Read.Version; version == nil || *version != 1 {
		t.Fatalf("external source lost: %+v", sessions)
	}
	for _, invalid := range []replicaTraceHistory{
		{{{{Kind: 1, Version: 1}, {Kind: 1, Version: 2}, {Kind: 0, Version: 1}}}},
		{{{{Kind: 1, Version: 1}, {Kind: 1, Version: 2}}}, {{{Kind: 0, Version: 1}}}},
		{{{{Kind: 0, Version: 9}}}},
		{{{{Kind: 1, Version: 1}}}, {{{Kind: 1, Version: 1}}}},
		{{{{Kind: 0, Initial: true, Version: 1}}}},
	} {
		if _, _, err := replicaTraceSessions(invalid); err == nil {
			t.Fatalf("accepted invalid generated history: %+v", invalid)
		}
	}
}

func TestReplicaTraceDeadlineKeepsCleanupIndependent(t *testing.T) {
	var disposed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Timeout int `json:"timeoutSec"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var result json.RawMessage
		switch request.Method {
		case "health":
			result = json.RawMessage(`{"status":"OK"}`)
		case "loadSpec":
			result = json.RawMessage(`{"sessionId":"abc","snapshotId":0,"specParameters":{"initTransitions":[{"index":0}],"nextTransitions":[{"index":0,"labels":["TakeStep"]}]}}`)
		case "assumeTransition":
			if request.Params.Timeout < 1 || request.Params.Timeout > 10 {
				t.Errorf("generation solver timeout exceeds ten-second budget: %d", request.Params.Timeout)
			}
			<-r.Context().Done()
			return
		case "disposeSpec":
			disposed.Store(true)
			result = json.RawMessage(`{"sessionId":"abc"}`)
		default:
			t.Errorf("unexpected RPC after timeout: %s", request.Method)
			return
		}
		if err := json.NewEncoder(w).Encode(struct {
			Version string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
		}{"2.0", request.ID, result}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := apalachebackend.New(server.URL, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	explorer := &apalacheExplorer{client: client, timeout: 30 * time.Second, closeDone: make(chan struct{})}
	defer explorer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	stats, err := enumerateReplicaTraceHistories(ctx, explorer, replicaTraceBounds{Replicas: 1, Depth: 3}, func(replicaTraceHistory) error {
		t.Fatal("timed-out generation published a history")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || stats.Exhausted || stats.Generated != 0 || !stats.TimedOut || !stats.SafeToRetry || !disposed.Load() {
		t.Fatalf("deadline/cleanup contract: stats=%+v disposed=%t error=%v", stats, disposed.Load(), err)
	}
}

// Construct complete small domains independently of the symbolic transition model.
func referenceReplicaTraceHistories(t *testing.T, bounds replicaTraceBounds) map[string]bool {
	t.Helper()
	history := make(replicaTraceHistory, bounds.Replicas)
	for r := range history {
		history[r] = [][]replicaTraceEvent{}
	}
	expected := make(map[string]bool)
	save := func() {
		data, err := json.Marshal(history)
		if err != nil {
			t.Fatal(err)
		}
		expected[string(data)] = true
	}
	if bounds.Pattern == 3 {
		for key := 0; key < 2; key++ {
			for version := uint64(1); version <= 2; version++ {
				history[0] = [][]replicaTraceEvent{{{Kind: 1, Key: key, Version: version}}}
				history[1] = [][]replicaTraceEvent{{{Key: key, Initial: true}, {Key: key, Version: version}}}
				save()
			}
		}
		return expected
	}
	if bounds.Pattern != 0 {
		for key := 0; key < 2; key++ {
			template := [2][2]replicaTraceEvent{
				{{Key: key, Initial: true}, {Kind: 1, Key: key}},
				{{Key: key, Initial: true}, {Kind: 1, Key: key}},
			}
			if bounds.Pattern == 1 {
				template[0][0].Key = 1 - key
				template[1][1].Key = 1 - key
			}
			for r := range history {
				history[r] = [][]replicaTraceEvent{make([]replicaTraceEvent, 2)}
			}
			var interleave func([2]int, int)
			interleave = func(positions [2]int, eventIndex int) {
				if eventIndex == 4 {
					save()
					return
				}
				for r := 0; r < 2; r++ {
					if positions[r] == 2 {
						continue
					}
					event := template[r][positions[r]]
					if event.Kind == 1 {
						event.Version = uint64(eventIndex + 1)
					}
					history[r][0][positions[r]] = event
					next := positions
					next[r]++
					interleave(next, eventIndex+1)
				}
			}
			interleave([2]int{}, 0)
		}
		return expected
	}
	save()
	if bounds.Depth < 2 || bounds.Transactions == 0 {
		return expected
	}
	for r := range history {
		var visit func([]replicaTraceEvent)
		visit = func(prefix []replicaTraceEvent) {
			history[r] = [][]replicaTraceEvent{prefix}
			save()
			if len(prefix) == min(bounds.Events, bounds.Depth-2) {
				return
			}
			for key := 0; key < 2; key++ {
				version := uint64(0)
				for _, prior := range prefix {
					if prior.Kind == 1 && prior.Key == key {
						version = prior.Version
					}
				}
				visit(append(slices.Clone(prefix), replicaTraceEvent{Key: key, Initial: version == 0, Version: version}))
				visit(append(slices.Clone(prefix), replicaTraceEvent{Kind: 1, Key: key, Version: uint64(len(prefix) + 1)}))
			}
		}
		visit([]replicaTraceEvent{})
		history[r] = [][]replicaTraceEvent{}
	}
	return expected
}
