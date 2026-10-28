package backend

import (
	"reflect"
	"testing"

	"github.com/rnbguy/dbcop/mockserver/exploration"
)

func TestFracturedReadOutcomesRespectCommitOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	for iso := ReadCommitted; iso <= Serializable; iso++ {
		t.Run(levelName(iso), func(t *testing.T) {
			runner := func(prefix []int) (exploration.Execution, error) {
				return runExplorationScenario(iso, "fractured-read", "replay", 1, prefix)
			}
			report, err := exploration.Systematic(runner, 128, 4)
			if err != nil || !report.Complete {
				t.Fatalf("incomplete admitted-choice enumeration: %+v error=%v", report, err)
			}
			want := map[string]bool{
				"reads=[0 0],aborted=false": true,
				"reads=[1 1],aborted=false": true,
			}
			if iso == ReadCommitted || iso == RepeatableRead {
				// RC permits initial then committed, never committed then initial.
				want["reads=[0 1],aborted=false"] = true
			}
			got := make(map[string]bool)
			for outcome := range report.Outcomes {
				got[outcome] = true
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("admitted fractured-read outcomes: got %v, want %v", got, want)
			}
		})
	}
}
