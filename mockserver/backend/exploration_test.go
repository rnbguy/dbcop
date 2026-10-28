package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/rnbguy/dbcop/mockserver/exploration"
)

// runExplorationScenario controls transaction order and re-executes the whole
// workload after every choice mutation; no read is patched into an old history.
func runExplorationScenario(iso Isolation, scenario, policy string, seed uint64, prefix []int) (result exploration.Execution, err error) {
	store := NewStore(seed, seed+1)
	defer store.Close()
	probability := 0.01
	if policy == "replay" {
		policy, probability = "sparse", 0
	}
	if err := store.SetReadPolicy(policy, probability); err != nil {
		return result, err
	}
	sessions := make(map[int]*Session)
	defer func() {
		for _, session := range sessions {
			session.SessionEnd()
		}
	}()
	session := func(id int) *Session {
		if sessions[id] == nil {
			sessions[id] = NewSession(store, iso, id)
		}
		return sessions[id]
	}
	commit := func(id int, writes ...StagedWrite) error {
		s := session(id)
		txn := s.beginTxn()
		for _, write := range writes {
			txn.stage(write)
		}
		return s.CommitTransaction(nil, nil)
	}
	write := func(column string, value int64) StagedWrite {
		return StagedWrite{Tab: "items", Pkey: "1", Col: column, Value: value}
	}
	if err := store.BeginInitialization(); err != nil {
		return result, err
	}
	if err := commit(9, write("x", 0), write("y", 0), write("z", 0)); err != nil {
		return result, err
	}
	if err := store.SealInitialState(); err != nil {
		return result, err
	}
	if len(prefix) != 0 {
		// At most four external reads occur in these finite scenarios.
		schedule := []int{-1, -1, -1, -1}
		if len(prefix) > len(schedule) {
			return result, fmt.Errorf("replay prefix exceeds scenario read bound")
		}
		copy(schedule, prefix)
		store.SetSchedule(schedule)
	}
	var observed []int64
	read := func(id int, column string) (int64, error) {
		s := session(id)
		s.beginTxn()
		key := cellKey("items", "1", column)
		// Enumerate through the real oracle to recover the ordered alternatives
		// for this small-test reference, without adding a production trace hook.
		var admitted []Version
		if s.admits(key, 0, true) {
			admitted = append(admitted, 0)
		}
		for _, version := range store.committedVersions(key) {
			if version.ID != 0 && s.admits(key, version.ID, false) {
				admitted = append(admitted, version.ID)
			}
		}
		value, selected, ok := s.oracleRead("items", "1", column)
		if !ok {
			return 0, fmt.Errorf("no admitted read for session %d variable %s", id, column)
		}
		choice := -1
		for index, version := range admitted {
			if version == selected {
				choice = index
				break
			}
		}
		if choice < 0 {
			return 0, fmt.Errorf("selected read was not admitted")
		}
		writer := 0
		if selected != 0 {
			info, exists := store.lookup(selected)
			if !exists {
				return 0, fmt.Errorf("selected writer missing")
			}
			writer = info.Session
		}
		result.Decisions = append(result.Decisions, exploration.Decision{
			Key: key, Reader: id, Writer: writer, Initial: selected == 0,
			Choice: choice, Alternatives: len(admitted),
		})
		observed = append(observed, value.(int64))
		return value.(int64), nil
	}
	var x, y, z int64
	aborted := false
	switch scenario {
	case "fractured-read":
		if err = commit(1, write("x", 1), write("y", 1)); err != nil {
			return result, err
		}
		if x, err = read(2, "x"); err != nil {
			return result, err
		}
		if y, err = read(2, "y"); err != nil {
			return result, err
		}
		if err = commit(2); err != nil {
			return result, err
		}
		result.Failed = x != y
	case "write-skew":
		if x, err = read(1, "x"); err != nil {
			return result, err
		}
		if x == 0 {
			if err = commit(1, write("y", 1)); err != nil {
				return result, err
			}
		}
		if y, err = read(2, "y"); err != nil {
			return result, err
		}
		if y == 0 {
			err = commit(2, write("x", 1))
		} else {
			err = commit(2)
		}
		if errors.Is(err, errCommitRejected) {
			aborted, err = true, nil
		}
		if err != nil {
			return result, err
		}
		if x, err = read(3, "x"); err != nil {
			return result, err
		}
		if y, err = read(3, "y"); err != nil {
			return result, err
		}
		if err = commit(3); err != nil {
			return result, err
		}
		result.Failed = x+y > 1
	case "dependency-chain":
		if err = commit(1, write("x", 1)); err != nil {
			return result, err
		}
		if x, err = read(2, "x"); err != nil {
			return result, err
		}
		if err = commit(2, write("y", x+1)); err != nil {
			return result, err
		}
		if y, err = read(3, "y"); err != nil {
			return result, err
		}
		if err = commit(3, write("z", y+1)); err != nil {
			return result, err
		}
		if z, err = read(4, "z"); err != nil {
			return result, err
		}
		if x, err = read(4, "x"); err != nil {
			return result, err
		}
		if err = commit(4); err != nil {
			return result, err
		}
		result.Failed = z == 3 && x == 0
	default:
		return result, fmt.Errorf("unknown exploration scenario %q", scenario)
	}
	result.Outcome = fmt.Sprintf("reads=%v,aborted=%t", observed, aborted)
	return result, nil
}

func TestExplorationPoliciesAgainstBoundedOracleReference(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	for iso := ReadCommitted; iso <= Serializable; iso++ {
		for _, scenario := range []string{"fractured-read", "write-skew", "dependency-chain"} {
			t.Run(levelName(iso)+"/"+scenario, func(t *testing.T) {
				runner := func(prefix []int) (exploration.Execution, error) {
					return runExplorationScenario(iso, scenario, "replay", 1, prefix)
				}
				reference, err := exploration.Systematic(runner, 128, 4)
				if err != nil || !reference.Complete {
					t.Fatalf("incomplete reference: %+v error=%v", reference, err)
				}
				if ((scenario == "write-skew" && iso == Serializable) || (scenario != "write-skew" && iso >= Causal)) && reference.Failures != 0 {
					t.Fatalf("reference admitted forbidden %s outcome at %s", scenario, levelName(iso))
				}
				for seed := uint64(1); seed <= 10; seed++ {
					for _, policy := range []string{"uniform", "sparse", "guided"} {
						const budget = 16
						var executions []exploration.Execution
						if policy == "guided" {
							report, err := exploration.Guided(runner, budget, 4, seed)
							if err != nil {
								t.Fatal(err)
							}
							executions = report.Corpus
							for _, execution := range report.Corpus {
								prefix := make([]int, len(execution.Decisions))
								for i, decision := range execution.Decisions {
									prefix[i] = decision.Choice
								}
								replayed, err := runner(prefix)
								if err != nil || !reflect.DeepEqual(execution, replayed) {
									t.Fatalf("corpus did not replay: got %+v error=%v want %+v", replayed, err, execution)
								}
							}
						} else {
							for run := 0; run < budget; run++ {
								execution, err := runExplorationScenario(iso, scenario, policy, seed*budget+uint64(run), nil)
								if err != nil {
									t.Fatal(err)
								}
								executions = append(executions, execution)
							}
						}
						outcomes := make(map[string]bool)
						features := make(map[exploration.Feature]bool)
						for _, execution := range executions {
							if _, exists := reference.Outcomes[execution.Outcome]; !exists {
								t.Fatalf("%s produced outcome outside complete reference: %+v", policy, execution)
							}
							outcomes[execution.Outcome] = true
							for _, feature := range exploration.SemanticFeatures(execution) {
								features[feature] = true
							}
						}
						data, err := json.Marshal(map[string]any{"level": levelName(iso), "scenario": scenario, "policy": policy, "seed": seed, "budget": budget, "outcomes": len(outcomes), "reference_outcomes": len(reference.Outcomes), "features": len(features), "reference_features": reference.FeatureCount})
						if err != nil {
							t.Fatal(err)
						}
						t.Logf("coverage_result %s", data)
					}
				}
			})
		}
	}
}
