package backend

import (
	"fmt"
	"reflect"
	"testing"
)

func TestSQLReductionContiguousRangesPreserveRequiredWrite(t *testing.T) {
	workload := reductionWorkload()
	writer := workload.Transactions[1]
	workload.Transactions = []SQLTransaction{}
	for index := range 7 {
		if index == 2 {
			workload.Transactions = append(workload.Transactions, writer)
			continue
		}
		workload.Transactions = append(workload.Transactions, SQLTransaction{
			ID: fmt.Sprintf("unrelated-%d", index), SessionID: index + 10,
			Statements: []SQLStatement{{ID: fmt.Sprintf("constant-%d", index), SQL: "SELECT 7"}},
			Finish:     "commit",
		})
	}
	result, err := RunSQLWorkload(workload, []int{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := CaptureSQLFailure(result.Workload, result)
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{1, 2, 128} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			reduced, err := MinimizeSQLArtifact(artifact, budget)
			if err != nil {
				t.Fatal(err)
			}
			if reduced.Artifact.Failure != artifact.Failure || !reflect.DeepEqual(reduced.Artifact.ObservedRows, []TypedRow{{{Kind: "int", Value: "1"}}}) {
				t.Fatalf("range reduction lost the required write's assertion: %+v", reduced)
			}
			if _, err := ReplaySQLArtifact(reduced.Artifact); err != nil {
				t.Fatalf("range-boundary result did not strictly replay: %v", err)
			}
			if budget < 128 {
				if reduced.Status != "budget_exhausted" || reduced.Trials != budget {
					t.Fatalf("range-boundary budget not honored: %+v", reduced)
				}
				return
			}
			if reduced.Status != "fixed_point" {
				t.Fatalf("unexhausted range reduction did not reach its fixed point: %+v", reduced)
			}
			want := []SQLTransaction{writer}
			want[0].Statements = []SQLStatement{writer.Statements[1]}
			if !reflect.DeepEqual(reduced.Artifact.Workload.Transactions, want) {
				t.Fatalf("required operation changed or unrelated ranges survived: got %+v want %+v", reduced.Artifact.Workload.Transactions, want)
			}
		})
	}
}
