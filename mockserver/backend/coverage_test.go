package backend

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/rnbguy/dbcop/mockserver/exploration"
)

func TestCommittedCoverageNormalizesIdentities(t *testing.T) {
	history := func(writer, reader int, x, y string, a, b Version) []LoggedTxn {
		return []LoggedTxn{
			{Session: writer, Writes: []CommittedWrite{{Key: x, ID: a}, {Key: y, ID: b}}},
			{Session: reader, Reads: []LoggedRead{{Key: x, WriteID: a}, {Key: y, WriteID: b}, {Key: x, Initial: true}}},
			{Session: reader, Reads: []LoggedRead{{Key: x, WriteID: a}, {Key: x, WriteID: a}}},
		}
	}
	first, err := CommittedCoverage(history(8, 13, "x", "y", 2, 3))
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := CommittedCoverage(history(104, 91, "renamed.a", "renamed.b", 200, 900))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, renamed) {
		t.Fatalf("renaming changed coverage: %#v != %#v", first, renamed)
	}
	if first.Transactions != 3 || first.Reads != 5 || len(first.Features) != 5 {
		t.Fatalf("wrong coverage counts: %#v", first)
	}
	var initial, differentVariableSameSource, repeatedSourceWithSO bool
	for _, observation := range first.Features {
		f := observation.Feature
		if f.Reader != 2 {
			t.Fatalf("wrong reader role: %#v", f)
		}
		initial = initial || (f.Initial && f.Writer == 0 && observation.FirstTransaction == 2)
		differentVariableSameSource = differentVariableSameSource || (f.HasPreviousRead && !f.SameVariable && f.SameSource)
		repeatedSourceWithSO = repeatedSourceWithSO || (f.SameVariable && f.SameSource && f.SessionOrdered && observation.FirstTransaction == 3)
	}
	if !initial || !differentVariableSameSource || !repeatedSourceWithSO {
		t.Fatalf("lost dependency distinctions: %#v", first.Features)
	}
}

func TestCommittedCoverageDoesNotRewardFreshVersions(t *testing.T) {
	history := []LoggedTxn{
		{Session: 1, Writes: []CommittedWrite{{Key: "x", ID: 1}}},
		{Session: 2, Reads: []LoggedRead{{Key: "x", WriteID: 1}}},
		{Session: 1, Writes: []CommittedWrite{{Key: "x", ID: 2}}},
		{Session: 2, Reads: []LoggedRead{{Key: "x", WriteID: 2}}},
		{Session: 1, Writes: []CommittedWrite{{Key: "x", ID: 3}}},
		{Session: 2, Reads: []LoggedRead{{Key: "x", WriteID: 3}}},
	}
	coverage, err := CommittedCoverage(history)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Transactions != 6 || coverage.Reads != 3 || len(coverage.Features) != 2 {
		t.Fatalf("fresh versions created fresh motifs: %#v", coverage)
	}
	for _, feature := range coverage.Features {
		if feature.FirstTransaction != 2 && feature.FirstTransaction != 4 {
			t.Fatalf("first discovery moved to repeated version: %#v", feature)
		}
	}
}

func TestCommittedCoverageRejectsInvalidSources(t *testing.T) {
	for name, history := range map[string][]LoggedTxn{
		"missing":   {{Session: 1, Reads: []LoggedRead{{Key: "x", WriteID: 4}}}},
		"future":    {{Session: 1, Reads: []LoggedRead{{Key: "x", WriteID: 4}}}, {Session: 2, Writes: []CommittedWrite{{Key: "x", ID: 4}}}},
		"wrong key": {{Session: 1, Writes: []CommittedWrite{{Key: "y", ID: 4}}}, {Session: 2, Reads: []LoggedRead{{Key: "x", WriteID: 4}}}},
		"duplicate": {{Session: 1, Writes: []CommittedWrite{{Key: "x", ID: 4}}}, {Session: 2, Writes: []CommittedWrite{{Key: "y", ID: 4}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CommittedCoverage(history); err == nil {
				t.Fatal("invalid history accepted")
			}
		})
	}
}

func TestCoverageProcedureExcludesRolledBackWork(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	if err := f.store.BeginInitialization(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"CREATE DATABASE coverage_fixture", "USE coverage_fixture", "CREATE TABLE items (id INT PRIMARY KEY, v INT)", "INSERT INTO items VALUES (1,0)", "CALL dbcop_seal_initial_state()", "SELECT v FROM items WHERE id=1", "BEGIN", "UPDATE items SET v=99 WHERE id=1", "ROLLBACK"} {
		f.query(t, query)
	}
	rows := f.query(t, "CALL dbcop_coverage()")
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("coverage result shape: %v", rows)
	}
	var coverage exploration.HistoryCoverage
	if err := json.Unmarshal([]byte(rows[0][0].(string)), &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.Transactions != 1 || len(coverage.Features) == 0 {
		t.Fatalf("rollback changed committed coverage: %#v", coverage)
	}
	for _, feature := range coverage.Features {
		if !feature.Feature.Initial {
			t.Fatalf("uncommitted write became a coverage source: %#v", feature)
		}
	}
}
