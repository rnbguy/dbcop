package backend

import (
	"math"
	"testing"
)

func TestReadPolicyValidation(t *testing.T) {
	store := NewStore(1, 2)
	t.Cleanup(store.Close)
	for _, test := range []struct {
		name        string
		probability float64
	}{
		{"unknown", 0.01}, {"sparse", -0.1}, {"sparse", 1.1},
		{"sparse", math.NaN()}, {"sparse", math.Inf(1)},
	} {
		if err := store.SetReadPolicy(test.name, test.probability); err == nil {
			t.Fatalf("accepted invalid policy %q probability %v", test.name, test.probability)
		}
	}
}

func TestSparseReadSelectionAndSeed(t *testing.T) {
	first, second := NewStore(41, 42), NewStore(41, 42)
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	for _, store := range []*Store{first, second} {
		if err := store.SetReadPolicy("sparse", 0.01); err != nil {
			t.Fatal(err)
		}
	}
	deviations := 0
	for i := 0; i < 10000; i++ {
		got := first.readIndex(4)
		if want := second.readIndex(4); got != want {
			t.Fatalf("same seeded decisions diverged at %d: %d != %d", i, got, want)
		}
		if got < 0 || got > 3 {
			t.Fatalf("selected nonexistent candidate %d", got)
		}
		if got != 3 {
			deviations++
		}
	}
	if deviations < 50 || deviations > 150 {
		t.Fatalf("1%% sparse selection produced %d deviations in 10000 decisions", deviations)
	}
	if err := first.SetReadPolicy("sparse", 0); err != nil {
		t.Fatal(err)
	}
	if got := first.readIndex(4); got != 3 {
		t.Fatalf("zero deviation selected %d, want newest", got)
	}
	if err := first.SetReadPolicy("sparse", 1); err != nil {
		t.Fatal(err)
	}
	seen := make(map[int]bool)
	for i := 0; i < 100; i++ {
		got := first.readIndex(4)
		if got == 3 {
			t.Fatal("certain deviation selected newest")
		}
		seen[got] = true
	}
	for _, candidate := range []int{0, 1, 2} {
		if !seen[candidate] {
			t.Fatalf("older admitted candidate %d unreachable", candidate)
		}
	}
	if got := first.readIndex(1); got != 0 {
		t.Fatalf("singleton deviation selected %d", got)
	}
}

func TestSparseOracleAdmissionAndOverrides(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	for _, iso := range []Isolation{ReadCommitted, RepeatableRead, AtomicRead, Causal, Prefix, SnapshotIsolation, Serializable} {
		store := NewStore(1, 2)
		t.Cleanup(store.Close)
		key := cellKey("items", "1", "value")
		store.cells[key] = &Cell{Versions: []CellVersion{{ID: 0, Value: 10}, {ID: 1, Value: 20}}}
		store.reg[1] = WriteInfo{Key: key, Session: 1, Seq: 0}
		store.log = []LoggedTxn{{Session: 1, Seq: 0, Writes: []CommittedWrite{{Key: key, ID: 1}}}}
		if err := store.SetReadPolicy("sparse", 1); err != nil {
			t.Fatal(err)
		}
		reader := NewSession(store, iso, 2)
		t.Cleanup(reader.SessionEnd)
		if value, version, ok := reader.oracleRead("items", "1", "value"); !ok || value != 10 || version != 0 {
			t.Fatalf("level %d: certain deviation did not select initial: %v %d %v", iso, value, version, ok)
		}
		reader.readLatest = true
		if value, version, ok := reader.oracleRead("items", "1", "value"); !ok || value != 20 || version != 1 {
			t.Fatalf("level %d: latest override failed: %v %d %v", iso, value, version, ok)
		}
		reader.readLatest = false
		store.SetSchedule([]int{1})
		if _, version, ok := reader.oracleRead("items", "1", "value"); !ok || version != 1 {
			t.Fatalf("level %d: replay override failed: %d %v", iso, version, ok)
		}
		store.SetSchedule(nil)
		if iso >= Causal {
			// Causal session order forbids forgetting this session's write.
			writer := NewSession(store, iso, 1)
			t.Cleanup(writer.SessionEnd)
			if _, version, ok := writer.oracleRead("items", "1", "value"); !ok || version != 1 {
				t.Fatalf("level %d: sparse policy bypassed admission: %d %v", iso, version, ok)
			}
		}
	}
}
