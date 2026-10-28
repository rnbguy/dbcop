//go:build cgo

package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type tlcObservation struct {
	history []LoggedTxn
	level   Isolation
	enabled bool
}

// Reuse the established Rust/TLC runner instead of introducing another JVM protocol.
func verifyTLCObservations(t testing.TB, observations []tlcObservation) {
	t.Helper()
	if len(observations) == 0 {
		t.Fatal("no observed histories to validate")
	}
	if os.Getenv("DBCOP_TLC_JAR") == "" {
		t.Fatal("real Apalache validation also requires DBCOP_TLC_JAR")
	}
	store := NewStore(1, 2)
	defer store.Close()
	var histories []json.RawMessage
	var expectations [][7]*bool
	seen := make(map[string]int)
	for _, observation := range observations {
		sessions := make([][]historyTxn, 0)
		indices := make(map[int]int)
		versions := map[uint64]uint64{0: 0}
		variables := make(map[uint64]uint64)
		for _, txn := range observation.history {
			index, ok := indices[txn.Session]
			if !ok {
				index = len(sessions)
				indices[txn.Session] = index
				sessions = append(sessions, nil)
			}
			events := store.transactionEvents(txn)
			for _, event := range events {
				cell := event.Read
				if cell == nil {
					cell = event.Write
				}
				variable, ok := variables[cell.Variable]
				if !ok {
					variable = uint64(len(variables))
					variables[cell.Variable] = variable
				}
				cell.Variable = variable
				if cell.Version != nil {
					version, ok := versions[*cell.Version]
					if !ok {
						version = uint64(len(versions))
						versions[*cell.Version] = version
					}
					cell.Version = &version
				}
			}
			sessions[index] = append(sessions[index], historyTxn{Events: events, Committed: true})
		}
		raw, err := json.Marshal(sessions)
		if err != nil {
			t.Fatal(err)
		}
		key := string(raw)
		index, ok := seen[key]
		if !ok {
			index = len(histories)
			seen[key] = index
			histories = append(histories, raw)
			expectations = append(expectations, [7]*bool{})
		}
		if previous := expectations[index][observation.level]; previous != nil && *previous != observation.enabled {
			t.Fatal("contradictory verdicts for an identical observed history")
		}
		expectations[index][observation.level] = &observation.enabled
	}
	cases := make([][2]any, len(histories))
	for i := range histories {
		cases[i] = [2]any{histories[i], expectations[i]}
	}
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "histories.json")
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
		t.Fatalf("Go/TLC validation: %v\n%s", err, output)
	}
	marker := fmt.Sprintf("DBCOP_GO_TLC_VERIFIED|%d\n", len(histories))
	if !strings.Contains(string(output), marker) {
		t.Fatalf("missing completed Go/TLC validation marker\n%s", output)
	}
	t.Logf("TLC/core/reference validated %d distinct observed histories", len(histories))
}

func externalReadHistory(request StepRequest, source ReadSource) []LoggedTxn {
	txn := LoggedTxn{Session: request.Session}
	for _, event := range request.Live {
		if event.IsRead {
			read := LoggedRead{Key: event.Read.Key, WriteID: event.Read.WriteID, Initial: event.Read.Initial}
			txn.Events = append(txn.Events, LoggedEvent{Read: &read})
		}
	}
	read := LoggedRead{Key: request.Key, WriteID: source.Version, Initial: source.Initial}
	txn.Events = append(txn.Events, LoggedEvent{Read: &read})
	history := append([]LoggedTxn(nil), request.Committed...)
	return append(history, txn)
}
