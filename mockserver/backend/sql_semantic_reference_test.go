package backend

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/rnbguy/dbcop/mockserver/exploration"
)

type semanticEvent struct {
	Kind   string
	Key    string
	Source ReplaySource
}

type semanticTransaction struct {
	ID     string
	Events []semanticEvent
}

// Normalize physical namespaces and runtime write IDs, but preserve every event
// and its order. Verify the raw export against the independently recorded trace.
func semanticHistory(t *testing.T, result SQLResult, controller *sqlReplayController) []semanticTransaction {
	t.Helper()
	var raw [][]historyTxn
	if err := json.Unmarshal(result.History, &raw); err != nil {
		t.Fatal(err)
	}
	iso, err := sqlIsolation(result.Workload.Isolation)
	if err != nil || !checkLevel(raw, levelName(iso)) {
		t.Fatalf("batch oracle rejected admitted SQL history: %s (%v)", result.History, err)
	}
	keys := make(map[uint64]string, len(result.Variables))
	for key, id := range result.Variables {
		keys[id] = key
	}
	versions := make([]Version, 0, len(controller.sources))
	for version := range controller.sources {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	sources := make(map[uint64]ReplaySource, len(versions))
	for index, version := range versions {
		sources[uint64(index+1)] = controller.sources[version]
	}
	bySession := make(map[int][]SQLTransaction)
	for _, txn := range result.Workload.Transactions {
		if txn.Finish == "commit" {
			bySession[txn.SessionID] = append(bySession[txn.SessionID], txn)
		}
	}
	ids := make([]int, 0, len(bySession))
	for id := range bySession {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	if len(raw) != len(ids) {
		t.Fatalf("abandoned/missing committed session: %s", result.History)
	}
	out := make([]semanticTransaction, 0)
	for sessionIndex, session := range raw {
		transactions := bySession[ids[sessionIndex]]
		if len(session) != len(transactions) {
			t.Fatalf("abandoned/missing committed transaction: %s", result.History)
		}
		for index, txn := range session {
			id := transactions[index].ID
			if !txn.Committed {
				t.Fatal("exported uncommitted transaction")
			}
			normalized := semanticTransaction{ID: id, Events: make([]semanticEvent, 0, len(txn.Events))}
			var choices []ReplayChoice
			for _, choice := range result.Choices {
				if choice.TransactionID == id {
					choices = append(choices, choice)
				}
			}
			readIndex := 0
			for _, event := range txn.Events {
				if (event.Read == nil) == (event.Write == nil) {
					t.Fatalf("invalid event: %+v", event)
				}
				if event.Write != nil {
					write := event.Write
					if write.Version == nil {
						t.Fatal("write lacks version")
					}
					source, ok := sources[*write.Version]
					if !ok || source.TransactionID != id || source.Key != keys[write.Variable] {
						t.Fatalf("write provenance/order changed: %+v source=%+v", write, source)
					}
					normalized.Events = append(normalized.Events, semanticEvent{"write", source.Key, source})
				} else {
					read := event.Read
					key := keys[read.Variable]
					source := ReplaySource{Initial: true, Key: key}
					if read.Version != nil {
						var ok bool
						source, ok = sources[*read.Version]
						if !ok {
							t.Fatalf("read of abandoned/missing write: %+v", read)
						}
					}
					if readIndex == len(choices) || choices[readIndex].Key != key || choices[readIndex].Selected != source {
						t.Fatalf("read event/trace order changed at %s/%d: %+v", id, readIndex, event)
					}
					readIndex++
					normalized.Events = append(normalized.Events, semanticEvent{"read", key, source})
				}
			}
			if readIndex != len(choices) {
				t.Fatalf("committed read disappeared: %s", id)
			}
			out = append(out, normalized)
		}
	}
	return out
}

func semanticColumn(t *testing.T, key string) string {
	t.Helper()
	var parts []string
	if err := json.Unmarshal([]byte(key), &parts); err != nil || len(parts) != 5 {
		t.Fatalf("canonical cell: %q %v", key, err)
	}
	if parts[4] == rowExistenceColumn {
		return "exists"
	}
	return parts[4]
}

func semanticReaderSignature(t *testing.T, choices []ReplayChoice) string {
	t.Helper()
	var reads []string
	for _, choice := range choices {
		if choice.TransactionID != "observe" {
			continue
		}
		source := choice.Selected.TransactionID
		if choice.Selected.Initial {
			source = "root"
		}
		reads = append(reads, semanticColumn(t, choice.Key)+"="+source)
	}
	return strings.Join(reads, ",")
}

// Admission is compared at a logical site conditioned on the same preceding
// sources, not by candidate enumeration order or independently sampled seeds.
func semanticAdmissions(choices []ReplayChoice) map[string]bool {
	out := make(map[string]bool)
	prior := make(map[string][]string)
	for _, choice := range choices {
		candidates := make([]string, 0, len(choice.Candidates))
		for _, source := range choice.Candidates {
			candidates = append(candidates, semanticJSON(source))
		}
		sort.Strings(candidates)
		context := append([]string(nil), prior[choice.TransactionID]...)
		sort.Strings(context)
		site := semanticJSON([]interface{}{choice.TransactionID, choice.StatementID, choice.Key, context, candidates})
		out[site] = true
		prior[choice.TransactionID] = append(prior[choice.TransactionID], semanticJSON([]interface{}{choice.Key, choice.Selected}))
	}
	return out
}

type semanticWeakCase struct {
	name      string
	columns   string
	initial   string
	mutation  string
	finish    string
	selectSQL string
	key       string
	want      map[string][]TypedRow
}

func semanticHandAdmission(t *testing.T, test semanticWeakCase, level Isolation, choices []ReplayChoice) {
	t.Helper()
	priorA := "root"
	for _, choice := range choices {
		column := semanticColumn(t, choice.Key)
		want := []string{"root"}
		if choice.TransactionID == "observe" {
			if column == "exists" && test.name == "historical_delete" {
				want = []string{"mutate", "root"}
			}
			if column == "a" {
				want = []string{"mutate", "root"}
			}
			if column == "b" {
				want = []string{"mutate", "root"}
				if priorA == "mutate" {
					want = []string{"mutate"}
				} else if level >= AtomicRead {
					want = []string{"root"}
				}
			}
		}
		got := make([]string, 0, len(choice.Candidates))
		for _, source := range choice.Candidates {
			name := source.TransactionID
			if source.Initial {
				name = "root"
			}
			got = append(got, name)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("hand-declared admission at %s/%s after a=%s: got %v want %v", choice.TransactionID, column, priorA, got, want)
		}
		if choice.TransactionID == "observe" && column == "a" {
			priorA = choice.Selected.TransactionID
			if choice.Selected.Initial {
				priorA = "root"
			}
		}
	}
}

func semanticHandEvents(t *testing.T, test semanticWeakCase, history []semanticTransaction, rows []TypedRow) {
	t.Helper()
	for _, txn := range history {
		var writes []string
		writing := false
		for _, event := range txn.Events {
			if event.Kind == "read" && writing {
				t.Fatalf("read appeared after the write stream: %+v", txn)
			}
			if event.Kind == "write" {
				writing = true
				writes = append(writes, semanticColumn(t, event.Key))
				if event.Source.StatementID == "" || event.Source.WriteOrdinal != len(writes) {
					t.Fatalf("write statement/ordinal provenance changed: %+v", event)
				}
			}
		}
		want := []string(nil)
		if txn.ID == "observe" && len(rows) != 0 {
			want = []string{"exists", "v"}
		}
		if txn.ID == "mutate" {
			switch test.name {
			case "historical_delete":
				want = []string{"exists"}
			case "historical_mixed_cells", "rc_forbids_source_regression":
				want = []string{"a", "b"}
			default:
				t.Fatalf("unexpected published mutation %s", test.name)
			}
		}
		if !reflect.DeepEqual(writes, want) {
			t.Fatalf("hand-declared write stream for %s: got %v want %v", txn.ID, writes, want)
		}
	}
}

func TestSQLSemanticMatrixWeakOutcomeSets(t *testing.T) {
	for _, test := range []semanticWeakCase{
		{name: "initial_absence", columns: "id BIGINT PRIMARY KEY,v BIGINT", selectSQL: "SELECT v FROM semanticdb.entries WHERE id=9", key: "9", want: map[string][]TypedRow{"exists=root": {}}},
		{name: "stored_null", columns: "id BIGINT PRIMARY KEY,v BIGINT", initial: "(1,NULL)", selectSQL: "SELECT v FROM semanticdb.entries WHERE id=1", key: "1", want: map[string][]TypedRow{"exists=root,id=root,v=root": {{{Kind: "null"}}}}},
		{name: "historical_delete", columns: "id BIGINT PRIMARY KEY,v BIGINT", initial: "(1,7)", mutation: "DELETE FROM semanticdb.entries WHERE id=1", selectSQL: "SELECT v FROM semanticdb.entries WHERE id=1", key: "1", want: map[string][]TypedRow{"exists=root,id=root,v=root": semanticInts(7), "exists=mutate": {}}},
		{name: "historical_mixed_cells", columns: "id BIGINT PRIMARY KEY,a BIGINT,b BIGINT,INDEX by_pair(a,b)", initial: "(1,1,0)", mutation: "UPDATE semanticdb.entries SET a=0,b=1 WHERE id=1", selectSQL: "SELECT id FROM semanticdb.entries WHERE a=1 AND b=1", key: "1", want: map[string][]TypedRow{"exists=root,a=mutate,b=mutate": {}, "exists=root,a=root,b=root": {}, "exists=root,a=root,b=mutate,id=root": semanticInts(1)}},
		{name: "rc_forbids_source_regression", columns: "id BIGINT PRIMARY KEY,a BIGINT,b BIGINT,INDEX by_pair(a,b)", initial: "(1,1,0)", mutation: "UPDATE semanticdb.entries SET a=0,b=1 WHERE id=1", selectSQL: "SELECT id FROM semanticdb.entries WHERE a=0 AND b=0", key: "1", want: map[string][]TypedRow{"exists=root,a=root,b=root": {}, "exists=root,a=root,b=mutate": {}, "exists=root,a=mutate,b=mutate": {}}},
		{name: "rolled_back_write", columns: "id BIGINT PRIMARY KEY,v BIGINT,INDEX by_v(v)", initial: "(1,7)", mutation: "UPDATE semanticdb.entries SET v=9 WHERE id=1", finish: "rollback", selectSQL: "SELECT v FROM semanticdb.entries WHERE id=1", key: "1", want: map[string][]TypedRow{"exists=root,id=root,v=root": semanticInts(7)}},
	} {
		for level := ReadCommitted; level <= Serializable; level++ {
			t.Run(test.name+"/"+levelName(level), func(t *testing.T) {
				isolation := levelName(level)
				if level == ReadCommitted {
					isolation = "read-committed"
				}
				wantHistories := test.want
				if level >= AtomicRead && (test.name == "historical_mixed_cells" || test.name == "rc_forbids_source_regression") {
					wantHistories = map[string][]TypedRow{
						"exists=root,a=root,b=root":     {},
						"exists=root,a=mutate,b=mutate": {},
					}
				}
				workload := SQLWorkload{SchemaVersion: 1, Isolation: isolation, Setup: []string{"CREATE DATABASE semanticdb", "CREATE TABLE semanticdb.entries (" + test.columns + ")", "CREATE TABLE semanticdb.sink(v BIGINT)"}, Transactions: []SQLTransaction{}, Assertion: SQLAssertion{ID: "observed", SQL: "SELECT v FROM semanticdb.sink ORDER BY v", BaselineRows: []TypedRow{}, WantRows: []TypedRow{}}}
				if test.initial != "" {
					workload.Setup = append(workload.Setup, "INSERT INTO semanticdb.entries VALUES"+test.initial)
				}
				if test.mutation != "" {
					finish := test.finish
					if finish == "" {
						finish = "commit"
					}
					workload.Transactions = append(workload.Transactions, SQLTransaction{ID: "mutate", SessionID: 1, Statements: []SQLStatement{{ID: "mutation", SQL: test.mutation}}, Finish: finish})
				}
				workload.Transactions = append(workload.Transactions, SQLTransaction{ID: "observe", SessionID: 2, Statements: []SQLStatement{{ID: "observation", SQL: "INSERT INTO semanticdb.sink " + test.selectSQL}}, Finish: "commit"})
				if len(workload.Transactions) > 4 {
					t.Fatal("transaction bound exceeded")
				}
				sessions := make(map[int]bool)
				for _, txn := range workload.Transactions {
					sessions[txn.SessionID] = true
				}
				if len(sessions) > 3 {
					t.Fatal("measured-session bound exceeded")
				}
				var outcomeSets [2]map[string]bool
				var admissionSets [2]map[string]bool
				for mode := range 2 {
					outcomeSets[mode] = make(map[string]bool)
					admissionSets[mode] = make(map[string]bool)
					seen := make(map[string]bool)
					run := func(prefix []int) (exploration.Execution, error) {
						// Systematic's initial nil means the empty strict prefix here,
						// never the public runner's sampling mode.
						fixed := append([]int{}, prefix...)
						controller := &sqlReplayController{prefix: fixed, prefixMode: true}
						if mode == 1 {
							controller.candidateKeysForTesting = semanticUniverse(t, map[string][]string{"entries": {test.key}, "sink": {}})
						}
						result, err := runSQLWorkload(workload, controller)
						if err != nil {
							return exploration.Execution{}, err
						}
						history := semanticHistory(t, result, controller)
						semanticHandAdmission(t, test, level, result.Choices)
						semanticHandEvents(t, test, history, result.ObservedRows)
						signature := semanticReaderSignature(t, result.Choices)
						want, ok := wantHistories[signature]
						if !ok {
							t.Fatalf("forbidden read history %q: %s", signature, result.History)
						}
						semanticWant(t, result.ObservedRows, want)
						seen[signature] = true
						for _, choice := range result.Choices {
							if test.finish == "rollback" {
								for _, source := range choice.Candidates {
									if source.TransactionID == "mutate" {
										t.Fatal("rolled-back write entered candidate admission")
									}
								}
							}
						}
						for site := range semanticAdmissions(result.Choices) {
							admissionSets[mode][site] = true
						}
						outcome := semanticJSON(struct {
							Rows    []TypedRow
							Error   string
							History []semanticTransaction
						}{result.ObservedRows, "", history})
						outcomeSets[mode][outcome] = true
						execution := exploration.Execution{Outcome: outcome}
						ambiguous := 0
						for _, choice := range result.Choices {
							if len(choice.Candidates) > 1 {
								ambiguous++
							}
							if len(choice.Candidates) > 3 {
								t.Fatal("candidate bound exceeded")
							}
							writer := 0
							if !choice.Selected.Initial {
								for _, txn := range workload.Transactions {
									if txn.ID == choice.Selected.TransactionID {
										writer = txn.SessionID
									}
								}
							}
							execution.Decisions = append(execution.Decisions, exploration.Decision{Key: choice.Key, Reader: choice.Reader, Writer: writer, Initial: choice.Selected.Initial, Choice: choice.Choice, Alternatives: len(choice.Candidates)})
						}
						if ambiguous > 3 {
							t.Fatal("ambiguous-read bound exceeded")
						}
						return execution, nil
					}
					report, err := exploration.Systematic(run, 10000, 128)
					if err != nil || !report.Complete || report.Runs >= 10000 {
						t.Fatalf("bounded SQL enumeration incomplete: runs=%d complete=%v err=%v", report.Runs, report.Complete, err)
					}
					if len(seen) != len(wantHistories) {
						t.Fatalf("missing hand-specified reachable histories: got %v want %v", seen, wantHistories)
					}
					t.Logf("storage weak matrix %s full_scan=%v runs=%d outcomes=%d complete=%v", test.name, mode == 1, report.Runs, len(outcomeSets[mode]), report.Complete)
				}
				if !reflect.DeepEqual(outcomeSets[0], outcomeSets[1]) {
					t.Fatalf("indexed/full-predicate-scan SQL/history outcome sets differ:\nindexed=%v\nscan=%v", outcomeSets[0], outcomeSets[1])
				}
				if !reflect.DeepEqual(admissionSets[0], admissionSets[1]) {
					t.Fatalf("indexed/full-predicate-scan admitted/source sets differ:\nindexed=%v\nscan=%v", admissionSets[0], admissionSets[1])
				}
			})
		}
	}
}
