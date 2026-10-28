package backend

import (
	"errors"
	"fmt"
	"testing"
)

// Count-only adapter for the original uniform/sparse regression tests.
func (s *Store) readIndex(n int) int {
	index, _ := s.selectReadIndex(n, false, nil)
	return index
}

func TestAgeStratifiedBucketsAndSeed(t *testing.T) {
	for _, test := range []struct {
		name    string
		n       int
		initial bool
		buckets [][]int
	}{
		{"root only", 1, true, [][]int{{0}}},
		{"one version without root", 1, false, [][]int{{0}}},
		{"root and one version", 2, true, [][]int{{0}, {1}}},
		{"two versions without root", 2, false, [][]int{{0}, {1}}},
		{"root and two versions", 3, true, [][]int{{0}, {1}, {2}}},
		{"singleton middle", 4, true, [][]int{{0}, {1}, {3}, {2}}},
		{"root and middle versions", 8, true, [][]int{{0}, {1}, {7}, {2, 3, 4, 5, 6}}},
		{"middle versions without root", 8, false, [][]int{{0}, {7}, {1, 2, 3, 4, 5, 6}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			first, second := NewStore(41, 42), NewStore(41, 42)
			t.Cleanup(first.Close)
			t.Cleanup(second.Close)
			for _, store := range []*Store{first, second} {
				if err := store.SetReadPolicy("age-stratified", 1); err != nil {
					t.Fatal(err)
				}
			}
			counts := make([]int, test.n)
			const samples = 12000
			for range samples {
				got, deviated := first.selectReadIndex(test.n, test.initial, nil)
				want, _ := second.selectReadIndex(test.n, test.initial, nil)
				if got != want || got < 0 || got >= test.n || deviated {
					t.Fatalf("invalid or nonreproducible age selection: %d, %d, %v", got, want, deviated)
				}
				counts[got]++
			}
			for _, bucket := range test.buckets {
				total := 0
				for _, index := range bucket {
					total += counts[index]
					want := samples / len(test.buckets) / len(bucket)
					if counts[index] < want*3/4 || counts[index] > want*5/4 {
						t.Fatalf("nonuniform bucket member %d: %v", index, counts)
					}
				}
				want := samples / len(test.buckets)
				if total < want*9/10 || total > want*11/10 {
					t.Fatalf("nonuniform bucket %v: %v", bucket, counts)
				}
			}
		})
	}
}

func TestBoundedDeviationSelectionBoundariesAndSeed(t *testing.T) {
	first, second := NewStore(41, 42), NewStore(41, 42)
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	if first.deviationBudget != 1 {
		t.Fatalf("default budget = %d, want 1", first.deviationBudget)
	}
	if err := first.SetDeviationBudget(-1); err == nil || first.deviationBudget != 1 {
		t.Fatal("invalid budget accepted or changed configuration")
	}
	for _, probability := range []float64{0, 0.35, 1} {
		for _, budget := range []int{0, 1, 3} {
			for _, store := range []*Store{first, second} {
				if err := store.SetReadPolicy("bounded-deviation", probability); err != nil {
					t.Fatal(err)
				}
				if err := store.SetDeviationBudget(budget); err != nil {
					t.Fatal(err)
				}
			}
			seen := [3]bool{}
			for range 100 {
				used := 0
				for read := range 10 {
					txn := &Txn{deviations: used}
					index, deviated := first.selectReadIndex(4, true, txn)
					want, wantDeviation := second.selectReadIndex(4, true, txn)
					if index != want || deviated != wantDeviation || index < 0 || index > 3 || (index < 3) != deviated {
						t.Fatalf("invalid/nonreproducible bounded selection: %d %v != %d %v", index, deviated, want, wantDeviation)
					}
					if probability == 1 && read < budget && !deviated {
						t.Fatal("certain deviation skipped an eligible read")
					}
					if deviated {
						used++
						seen[index] = true
					}
					if used > budget || (probability == 0 && used != 0) {
						t.Fatalf("budget exceeded: used %d, budget %d, probability %g", used, budget, probability)
					}
					if index, deviated := first.selectReadIndex(1, true, txn); index != 0 || deviated {
						t.Fatal("singleton consumed deviation budget")
					}
					second.selectReadIndex(1, true, txn)
				}
			}
			if probability > 0 && budget > 0 && seen != [3]bool{true, true, true} {
				t.Fatalf("older-source diversity missing: %v", seen)
			}
		}
	}
	first.weakRandom = false
	if index, deviated := first.selectReadIndex(4, true, nil); index != 0 || deviated {
		t.Fatal("disabled random selection changed")
	}
}

func policySession(t *testing.T, policy string, iso Isolation) (*Store, *Session) {
	t.Helper()
	store := NewStore(41, 42)
	t.Cleanup(store.Close)
	if err := store.SetReadPolicy(policy, 1); err != nil {
		t.Fatal(err)
	}
	writer := LoggedTxn{Session: 1, Seq: 0}
	for i, col := range []string{"a", "b", "c", "d", "e"} {
		key, id := cellKey("items", "1", col), Version(i+1)
		store.cells[key] = &Cell{Versions: []CellVersion{{Value: 10}, {ID: id, Value: 20}}}
		store.reg[id] = WriteInfo{Key: key, Session: 1, Seq: 0}
		writer.Writes = append(writer.Writes, CommittedWrite{Key: key, ID: id})
	}
	store.log = []LoggedTxn{writer}
	store.nextID, store.seq = 5, 1
	session := NewSession(store, iso, 2)
	t.Cleanup(session.SessionEnd)
	return store, session
}

func policyRead(t *testing.T, session *Session, column string, older bool) {
	t.Helper()
	value, version, ok := session.oracleRead("items", "1", column)
	if !ok || (version == 0) != older || (older && value != 10) || (!older && value != 20) {
		t.Fatalf("read %s = %v version %d, %v; older=%v", column, value, version, ok, older)
	}
}

func TestNativePolicyAdmissionAndOverrides(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	for _, policy := range []string{"age-stratified", "bounded-deviation"} {
		for _, iso := range []Isolation{ReadCommitted, RepeatableRead, AtomicRead, Causal, Prefix, SnapshotIsolation, Serializable} {
			t.Run(fmt.Sprintf("%s/%d", policy, iso), func(t *testing.T) {
				store, session := policySession(t, policy, iso)
				if _, _, ok := session.oracleRead("items", "1", "a"); !ok || store.Admissions()[0].Admitted != 2 {
					t.Fatal("policy did not select from the full admitted root/version set")
				}
				beginSessionTransaction(t, session)
				session.readLatest = true
				policyRead(t, session, "a", false)
				session.readLatest = false
				store.SetSchedule([]int{0})
				// The oracle may exclude root after the first observation.
				value, version, ok := session.oracleRead("items", "1", "a")
				if !ok || value != 20 || version != 1 {
					t.Fatal("replay bypassed admission")
				}
				store.SetSchedule(nil)
				if session.liveTxn().checkpoint().deviations != 0 {
					t.Fatal("explicit overrides spent budget")
				}
				if _, _, ok := session.oracleRead("items", "1", "b"); !ok {
					t.Fatal("policy failed to choose an admitted source")
				}
				for _, admission := range store.Admissions() {
					if admission.Admitted < 1 {
						t.Fatalf("selected outside admitted set: %+v", admission)
					}
				}
			})
		}
	}
}

func TestBoundedDeviationTransactionBoundaries(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	for _, end := range []string{"commit", "rollback", "disconnect", "engine reset"} {
		t.Run(end, func(t *testing.T) {
			_, session := policySession(t, "bounded-deviation", ReadCommitted)
			tx := beginSessionTransaction(t, session)
			policyRead(t, session, "a", true)
			policyRead(t, session, "b", false)
			if used := session.liveTxn().checkpoint().deviations; used != 1 {
				t.Fatalf("spent %d deviations, want 1", used)
			}
			switch end {
			case "commit":
				if err := session.CommitTransaction(nil, tx); err != nil {
					t.Fatal(err)
				}
			case "rollback":
				if err := session.Rollback(nil, tx); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				session.SessionEnd()
			case "engine reset":
				session.SetTransaction(nil)
			}
			assertSessionTransactionEnded(t, session)
			beginSessionTransaction(t, session)
			policyRead(t, session, "c", true)
		})
	}
	_, session := policySession(t, "bounded-deviation", ReadCommitted)
	policyRead(t, session, "a", true)
	policyRead(t, session, "b", true)
}

func TestBoundedDeviationRestoresStatementAndSavepointPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	store, session := policySession(t, "bounded-deviation", ReadCommitted)
	if err := store.SetDeviationBudget(2); err != nil {
		t.Fatal(err)
	}
	tx := beginSessionTransaction(t, session)
	policyRead(t, session, "a", true)
	if err := session.CreateSavepoint(nil, tx, "prefix"); err != nil {
		t.Fatal(err)
	}
	writer := &rowWriter{sess: session}
	writer.StatementBegin(nil)
	policyRead(t, session, "b", true)
	policyRead(t, session, "c", false)
	if err := writer.DiscardChanges(nil, errors.New("statement failure")); err != nil {
		t.Fatal(err)
	}
	if used := session.liveTxn().checkpoint().deviations; used != 1 {
		t.Fatalf("statement rollback retained %d deviations, want 1", used)
	}
	policyRead(t, session, "b", true)
	if err := session.CreateSavepoint(nil, tx, "spent"); err != nil {
		t.Fatal(err)
	}
	policyRead(t, session, "c", false)
	if err := session.RollbackToSavepoint(nil, tx, "spent"); err != nil {
		t.Fatal(err)
	}
	policyRead(t, session, "c", false)
	if err := session.RollbackToSavepoint(nil, tx, "prefix"); err != nil {
		t.Fatal(err)
	}
	policyRead(t, session, "d", true)
	policyRead(t, session, "e", false)
	if err := session.ReleaseSavepoint(nil, tx, "prefix"); err != nil {
		t.Fatal(err)
	}
	if used := session.liveTxn().checkpoint().deviations; used != 2 {
		t.Fatalf("savepoint rollback/release lost retained usage: %d", used)
	}
	if err := session.CommitTransaction(nil, tx); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedDeviationOverridesDoNotSpendBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	store, session := policySession(t, "bounded-deviation", ReadCommitted)
	tx := beginSessionTransaction(t, session)
	store.SetSchedule([]int{0})
	policyRead(t, session, "a", true)
	store.SetSchedule(nil)
	policyRead(t, session, "b", true)
	policyRead(t, session, "c", false)
	if used := session.liveTxn().checkpoint().deviations; used != 1 {
		t.Fatalf("scheduled root spent budget: %d", used)
	}
	if err := session.Rollback(nil, tx); err != nil {
		t.Fatal(err)
	}
	beginSessionTransaction(t, session)
	session.readLatest = true
	policyRead(t, session, "a", false)
	session.readLatest = false
	if used := session.liveTxn().checkpoint().deviations; used != 0 {
		t.Fatalf("latest override spent budget: %d", used)
	}
}

func TestBoundedDeviationIgnoresLocalAndSingletonReads(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	_, session := policySession(t, "bounded-deviation", ReadCommitted)
	beginSessionTransaction(t, session)
	session.liveTxn().stage(StagedWrite{Tab: "items", Pkey: "1", Col: "local", Value: 30})
	if value, version, ok := session.oracleRead("items", "1", "local"); !ok || value != 30 || version != 0 {
		t.Fatalf("read-your-writes changed: %v %d %v", value, version, ok)
	}
	if value, version, ok := session.oracleRead("items", "1", "missing"); !ok || value != nil || version != 0 {
		t.Fatalf("singleton root read failed: %v %d %v", value, version, ok)
	}
	if used := session.liveTxn().checkpoint().deviations; used != 0 {
		t.Fatalf("ineligible reads consumed budget: %d", used)
	}
	policyRead(t, session, "a", true)
	policyRead(t, session, "b", false)
}
