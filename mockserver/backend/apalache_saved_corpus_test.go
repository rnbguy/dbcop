//go:build cgo

package backend

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type savedApalacheCase struct {
	Name     string         `json:"name"`
	History  [][]historyTxn `json:"history"`
	Expected [7]bool        `json:"expected"`
}

func savedApalacheArrival(t *testing.T, fixture savedApalacheCase) []LoggedTxn {
	t.Helper()
	history := make(replicaTraceHistory, len(fixture.History))
	for s, session := range fixture.History {
		history[s] = make([][]replicaTraceEvent, len(session))
		for h, txn := range session {
			if !txn.Committed {
				t.Fatalf("uncommitted saved transaction: %s", fixture.Name)
			}
			history[s][h] = make([]replicaTraceEvent, len(txn.Events))
			for p, event := range txn.Events {
				if (event.Read == nil) == (event.Write == nil) {
					t.Fatalf("invalid saved event: %s", fixture.Name)
				}
				cell, kind := event.Read, 0
				if event.Write != nil {
					cell, kind = event.Write, 1
				}
				if cell.Variable > 1 || kind == 1 && cell.Version == nil {
					t.Fatalf("event outside saved operational domain: %s", fixture.Name)
				}
				value := replicaTraceEvent{Kind: kind, Key: int(cell.Variable), Initial: cell.Version == nil}
				if cell.Version != nil {
					value.Version = *cell.Version
				}
				history[s][h][p] = value
			}
		}
	}
	_, arrival, err := replicaTraceSessions(history)
	if err != nil {
		t.Fatalf("saved history %s: %v", fixture.Name, err)
	}
	log := make([]LoggedTxn, 0, len(arrival))
	for _, position := range arrival {
		var events []LoggedEvent
		for _, event := range fixture.History[position.session][position.height].Events {
			if event.Write != nil {
				events = append(events, apalacheTestWrite(fmt.Sprintf("saved:%d", event.Write.Variable), Version(*event.Write.Version)))
			} else if event.Read.Version == nil {
				events = append(events, apalacheTestRoot(fmt.Sprintf("saved:%d", event.Read.Variable)))
			} else {
				events = append(events, apalacheTestRead(fmt.Sprintf("saved:%d", event.Read.Variable), Version(*event.Read.Version)))
			}
		}
		log = append(log, apalacheTestTxn(position.session+1, events...))
	}
	return log
}

func TestApalacheSavedCorpusAgreement(t *testing.T) {
	explorer := apalacheTestExplorer(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "crates", "core", "tests", "fixtures", "apalache.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []savedApalacheCase
	if err := jsonv2.Unmarshal(data, &fixtures, jsonv2.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("saved corpus is empty")
	}
	var verdicts [7][2]int
	names := make(map[string]bool)
	for index, fixture := range fixtures {
		if fixture.Name == "" || names[fixture.Name] {
			t.Fatalf("missing or duplicate saved case name %q", fixture.Name)
		}
		names[fixture.Name] = true
		arrival := savedApalacheArrival(t, fixture)
		committed := arrival
		var proposed LoggedTxn
		if len(arrival) == 0 {
			// Empty histories have no operation: an empty initial commit is isolation-neutral.
			proposed = apalacheTestTxn(1)
		} else {
			committed, proposed = arrival[:len(arrival)-1], arrival[len(arrival)-1]
		}
		for level := ReadCommitted; level <= Serializable; level++ {
			expected := fixture.Expected[level]
			if native := checkLevel(fixture.History, levelName(level)); native != expected {
				t.Fatalf("saved core mismatch case=%s level=%s expected=%t actual=%t raw=%s", fixture.Name, levelName(level), expected, native, data)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			result, stepErr := explorer.Step(ctx, StepRequest{
				Kind: "commit", Isolation: level, Session: proposed.Session,
				Committed: committed, Transaction: proposed,
			})
			cancel()
			if stepErr != nil {
				t.Fatalf("saved Apalache execution error case=%s level=%s: %v", fixture.Name, levelName(level), stepErr)
			}
			if result.Enabled != expected {
				raw, marshalErr := json.Marshal(fixture)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				t.Fatalf("saved Apalache mismatch case=%s level=%s expected=%t actual=%t raw=%s", fixture.Name, levelName(level), expected, result.Enabled, raw)
			}
			bucket := 0
			if expected {
				bucket = 1
			}
			verdicts[level][bucket]++
		}
		if (index+1)%16 == 0 || index+1 == len(fixtures) {
			t.Logf("saved corpus progress=%d/%d (%d%%), all seven core/Apalache verdicts matched", index+1, len(fixtures), 100*(index+1)/len(fixtures))
		}
	}
	for level := ReadCommitted; level <= Serializable; level++ {
		if verdicts[level][1] == 0 || level != ReadCommitted && verdicts[level][0] == 0 {
			t.Fatalf("saved corpus lacks required positive/negative coverage at %s: %v", levelName(level), verdicts[level])
		}
		t.Logf("saved corpus level=%s YES=%d NO=%d", levelName(level), verdicts[level][1], verdicts[level][0])
	}
}
