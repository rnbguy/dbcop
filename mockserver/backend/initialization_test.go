package backend

import (
	"reflect"
	"testing"
	"time"
)

func commitInitializationTestWrites(t *testing.T, session *Session, writes ...StagedWrite) {
	t.Helper()
	tx := beginSessionTransaction(t, session)
	for _, write := range writes {
		session.liveTxn().stage(write)
	}
	if err := session.CommitTransaction(nil, tx); err != nil {
		t.Fatal(err)
	}
}

func TestInitializationLatestAndBaselineAllLevels(t *testing.T) {
	for iso := ReadCommitted; iso <= Serializable; iso++ {
		t.Run(levelName(iso), func(t *testing.T) {
			store := NewStore(1, 2)
			t.Cleanup(store.Close)
			if err := store.BeginInitialization(); err != nil {
				t.Fatal(err)
			}
			store.SetSchedule([]int{0}) // Loading must ignore the weak absence choice.
			loader := NewSession(store, iso, 1)
			t.Cleanup(loader.SessionEnd)
			commitInitializationTestWrites(t, loader,
				StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: int64(10)},
				StagedWrite{Tab: "items", Pkey: "1", Col: "nullable", Value: nil},
				StagedWrite{Tab: "items", Pkey: "1", Col: rowExistenceColumn, Value: true},
				StagedWrite{Tab: "items", Pkey: "deleted", Col: rowExistenceColumn, Value: false},
			)
			tx := beginSessionTransaction(t, loader)
			if value, version, ok := loader.oracleRead("items", "1", "value"); !ok || value != int64(10) || version == 0 {
				t.Fatalf("loading read chose an absent or old value: value=%v version=%d ok=%v", value, version, ok)
			}
			if value, _, ok := loader.oracleRead("items", "1", rowExistenceColumn); !ok || value != true {
				t.Fatal("loading read lost a committed row")
			}
			loader.liveTxn().stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: int64(20)})
			if value, _, ok := loader.oracleRead("items", "1", "value"); !ok || value != int64(20) {
				t.Fatal("loading read lost its staged write")
			}
			if err := loader.CommitTransaction(nil, tx); err != nil {
				t.Fatal(err)
			}
			if value, _, ok := loader.oracleRead("items", "1", "value"); !ok || value != int64(20) {
				t.Fatal("loading read did not select the newest committed value")
			}
			if len(store.Admissions()) != 0 || store.schedPos != 0 || len(store.oracles.states) != 0 {
				t.Fatal("loading consumed a weak choice or evaluated measured history")
			}
			key := cellKey("items", "1", "value")
			chain := store.cells[key].Versions
			nextID := store.nextID
			set := store.oracles
			if err := store.SealInitialState(); err != nil {
				t.Fatal(err)
			}
			root := store.cells[key].Versions
			if len(root) != 1 || root[0].ID != 0 || root[0].Value != int64(20) || &root[0] != &chain[0] {
				t.Fatalf("seal did not reuse the chain for the latest initial value: %v", root)
			}
			if chain[1].ID != 0 || chain[1].Value != nil {
				t.Fatal("seal retained preparation values in the unused backing chain")
			}
			if store.oracles != set || store.nextID != nextID || store.seq != 0 || len(store.reg) != 0 || len(store.logSnapshot()) != 0 {
				t.Fatal("seal retained preparation history, reset version IDs, or replaced cleanup ownership")
			}
			reader := NewSession(store, iso, 2)
			t.Cleanup(reader.SessionEnd)
			tx = beginSessionTransaction(t, reader)
			for _, read := range []struct {
				pkey   string
				column string
				value  interface{}
			}{
				{"1", "value", int64(20)},
				{"1", "nullable", nil},
				{"1", rowExistenceColumn, true},
				{"deleted", rowExistenceColumn, false},
				{"missing", "value", nil},
			} {
				value, version, ok := reader.oracleRead("items", read.pkey, read.column)
				if !ok || version != 0 || value != read.value {
					t.Fatalf("initial root (%s,%s): value=%v version=%d ok=%v", read.pkey, read.column, value, version, ok)
				}
			}
			if err := reader.CommitTransaction(nil, tx); err != nil {
				t.Fatal(err)
			}
			for _, admission := range store.Admissions() {
				if admission.Committed != 0 || admission.Admitted != 1 || !admission.Initial {
					t.Fatalf("a root was counted or tested as a positive committed version: %+v", admission)
				}
			}
			writer := NewSession(store, iso, 3)
			t.Cleanup(writer.SessionEnd)
			commitInitializationTestWrites(t, writer,
				StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: int64(30)},
			)
			if store.nextID <= nextID {
				t.Fatal("post-seal write reused a preparation version ID")
			}
			for choice, want := range []int64{20, 30} {
				store.SetSchedule([]int{choice})
				observer := NewSession(store, iso, 4+choice)
				value, version, ok := observer.oracleRead("items", "1", "value")
				if !ok || value != want || (version == 0) != (choice == 0) {
					t.Fatalf("weak choice %d: value=%v version=%d ok=%v", choice, value, version, ok)
				}
				admissions := store.Admissions()
				last := admissions[len(admissions)-1]
				if last.Committed != 1 || last.Admitted != 2 {
					t.Fatalf("post-seal candidates contain preparation absence or versions: %+v", last)
				}
			}
			before := store.logSnapshot()
			admissions := store.Admissions()
			versions := store.committedVersions(key)
			if err := store.SealInitialState(); err == nil {
				t.Fatal("a second seal must be rejected")
			}
			if !reflect.DeepEqual(store.logSnapshot(), before) || !reflect.DeepEqual(store.Admissions(), admissions) || !reflect.DeepEqual(store.committedVersions(key), versions) {
				t.Fatal("a rejected second seal lost measured history or data")
			}
		})
	}
}

func TestInitializationSealResetsNativePrefixes(t *testing.T) {
	for iso := ReadCommitted; iso <= Serializable; iso++ {
		t.Run(levelName(iso), func(t *testing.T) {
			store := NewStore(1, 2)
			t.Cleanup(store.Close)
			if err := store.BeginInitialization(); err != nil {
				t.Fatal(err)
			}
			x := store.Write("items", "1", "x", int64(10))
			y := store.Write("items", "1", "y", int64(20))
			store.appendLog(1, 0, []CommittedWrite{
				{Key: cellKey("items", "1", "x"), ID: x},
				{Key: cellKey("items", "1", "y"), ID: y},
			}, nil)
			session := NewSession(store, iso, 2)
			t.Cleanup(session.SessionEnd)
			if !session.checkRead(cellKey("items", "1", "x"), x, false, true) {
				t.Fatal("could not retain a preparation native prefix")
			}
			old := store.oracles.states[iso]
			if old == nil || old.session != 2 || old.revision != 1 {
				t.Fatal("preparation oracle was not synchronized")
			}
			if err := store.SealInitialState(); err != nil {
				t.Fatal(err)
			}
			if len(store.oracles.states) != 0 || len(store.variables) != 0 {
				t.Fatal("seal retained native history or its variable registry")
			}
			tx := beginSessionTransaction(t, session)
			for column, want := range map[string]int64{"x": 10, "y": 20} {
				value, version, ok := session.oracleRead("items", "1", column)
				if !ok || value != want || version != 0 {
					t.Fatalf("old native prefix constrained sealed root %s: value=%v version=%d ok=%v", column, value, version, ok)
				}
			}
			if err := session.CommitTransaction(nil, tx); err != nil {
				t.Fatal(err)
			}
			if current := store.oracles.states[iso]; current == old || current.revision != 1 {
				t.Fatal("preparation state was reused or promoted after sealing")
			}
			if log := store.logSnapshot(); len(log) != 1 || len(log[0].Writes) != 0 || len(log[0].Reads) != 2 {
				t.Fatal("old native history leaked into the measured transaction")
			}
		})
	}
}

func TestInitializationSealWaitsForCommittedData(t *testing.T) {
	store := NewStore(1, 2)
	t.Cleanup(store.Close)
	if err := store.BeginInitialization(); err != nil {
		t.Fatal(err)
	}
	loader := NewSession(store, Serializable, 1)
	t.Cleanup(loader.SessionEnd)
	tx := beginSessionTransaction(t, loader)
	loader.liveTxn().stage(StagedWrite{Tab: "items", Pkey: "1", Col: "x", Value: int64(10)})
	loader.liveTxn().stage(StagedWrite{Tab: "items", Pkey: "1", Col: "y", Value: int64(20)})
	started := make(chan struct{})
	sealed := make(chan error, 1)
	go func() {
		close(started)
		sealed <- store.SealInitialState()
	}()
	<-started
	select {
	case err := <-sealed:
		t.Fatalf("seal passed an active loading transaction: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := loader.CommitTransaction(nil, tx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sealed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("seal did not resume after the loading transaction committed")
	}
	reader := NewSession(store, Serializable, 2)
	t.Cleanup(reader.SessionEnd)
	tx = beginSessionTransaction(t, reader)
	for column, want := range map[string]int64{"x": 10, "y": 20} {
		if value, version, ok := reader.oracleRead("items", "1", column); !ok || value != want || version != 0 {
			t.Fatalf("seal exposed a partial dataset: column=%s value=%v version=%d ok=%v", column, value, version, ok)
		}
	}
	if err := reader.CommitTransaction(nil, tx); err != nil {
		t.Fatal(err)
	}
}

func TestInitializationLifecycleAndChoiceState(t *testing.T) {
	store := NewStore(1, 2)
	t.Cleanup(store.Close)
	if err := store.SealInitialState(); err == nil {
		t.Fatal("sealing must require explicit initialization opt-in")
	}
	if err := store.BeginInitialization(); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginInitialization(); err == nil {
		t.Fatal("repeated initialization must be rejected")
	}
	store.SetSchedule([]int{1, 0})
	store.replayChoice(2)
	store.recordAdmission("preparation", 1, 1, false, 1)
	rng := store.rng
	want := newLockedRand(1, 2)
	if store.rng.Intn(17) != want.Intn(17) {
		t.Fatal("seeded streams differ before sealing")
	}
	if err := store.SealInitialState(); err != nil {
		t.Fatal(err)
	}
	if store.rng != rng || store.rng.Intn(17) != want.Intn(17) {
		t.Fatal("seal reset the user's RNG stream")
	}
	if store.schedPos != 0 || store.replayChoice(2) != 1 || len(store.Admissions()) != 0 {
		t.Fatal("seal did not rewind the retained schedule or clear preparation admissions")
	}
	if err := store.BeginInitialization(); err == nil {
		t.Fatal("even an empty sealed store must not restart initialization")
	}
	populated := NewStore(1, 2)
	t.Cleanup(populated.Close)
	id := populated.Write("items", "1", "x", int64(10))
	populated.appendLog(1, 0, []CommittedWrite{{Key: cellKey("items", "1", "x"), ID: id}}, nil)
	if err := populated.BeginInitialization(); err == nil {
		t.Fatal("initialization must not discard existing data or history")
	}
	populated.SetSchedule([]int{0})
	reader := NewSession(populated, ReadCommitted, 2)
	if value, version, ok := reader.oracleRead("items", "1", "x"); !ok || value != nil || version != 0 {
		t.Fatal("default NewStore weak reads changed without initialization opt-in")
	}
}
