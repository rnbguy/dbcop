package exploration

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestSystematicEnumeratesConditionalTree(t *testing.T) {
	seen := make(map[string]int)
	run := func(prefix []int) (Execution, error) {
		execution := Execution{Outcome: "same value"}
		choices := make([]int, 0, 3)
		choose := func(alternatives int) int {
			index := len(choices)
			choice := alternatives - 1
			if index < len(prefix) {
				choice = prefix[index]
			}
			if choice < 0 || choice >= alternatives {
				t.Fatalf("prefix %v has invalid choice %d at decision %d", prefix, choice, index)
			}
			choices = append(choices, choice)
			execution.Decisions = append(execution.Decisions, Decision{
				Key: fmt.Sprintf("key%d", index), Reader: 1, Writer: choice + 2,
				Choice: choice, Alternatives: alternatives,
			})
			return choice
		}
		switch choose(3) {
		case 0:
			if choose(2) == 0 {
				choose(2)
			}
		case 2:
			if choose(3) == 0 {
				choose(1)
			}
		}
		path := fmt.Sprint(choices)
		seen[path]++
		if path == "[2 2]" {
			execution.Outcome = "assertion"
			execution.Failed = true
		}
		return execution, nil
	}
	report, err := Systematic(run, 7, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"[0 0 0]": 1, "[0 0 1]": 1, "[0 1]": 1,
		"[1]": 1, "[2 0 0]": 1, "[2 1]": 1, "[2 2]": 1,
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("visited leaves = %v, want %v", seen, want)
	}
	if !report.Complete || report.Runs != 7 || report.Evaluated != 7 || report.Failures != 1 || report.FirstFailure != 1 {
		t.Fatalf("unexpected exhaustive report: %+v", report)
	}
	if !reflect.DeepEqual(report.Outcomes, map[string]int{"same value": 6, "assertion": 1}) {
		t.Fatalf("outcomes = %v", report.Outcomes)
	}
	if report.FeatureCount == 0 || len(report.Corpus) == 0 {
		t.Fatalf("missing semantic coverage or replay corpus: %+v", report)
	}
}

func TestSystematicHonorsRunBound(t *testing.T) {
	for _, maxRuns := range []int{1, 2} {
		t.Run(fmt.Sprint(maxRuns), func(t *testing.T) {
			seen := make(map[int]int)
			run := func(prefix []int) (Execution, error) {
				choice := 1
				if len(prefix) > 0 {
					choice = prefix[0]
				}
				seen[choice]++
				return Execution{Decisions: []Decision{{Key: "x", Reader: 1, Writer: choice + 2, Choice: choice, Alternatives: 2}}}, nil
			}
			report, err := Systematic(run, maxRuns, 1)
			if err != nil {
				t.Fatal(err)
			}
			if report.Runs != maxRuns || report.Evaluated != maxRuns || report.Complete != (maxRuns == 2) {
				t.Fatalf("unexpected bounded report: %+v", report)
			}
			if seen[1] != 1 || seen[0] != maxRuns-1 {
				t.Fatalf("visited choices = %v", seen)
			}
		})
	}
}

func TestSystematicCompletesWithoutDecisions(t *testing.T) {
	report, err := Systematic(func(prefix []int) (Execution, error) {
		if len(prefix) != 0 {
			t.Fatalf("unexpected prefix %v", prefix)
		}
		return Execution{Outcome: "done"}, nil
	}, 1, 0)
	if err != nil || !report.Complete || report.Runs != 1 || report.Evaluated != 1 || report.Outcomes["done"] != 1 {
		t.Fatalf("empty execution report = %+v, error = %v", report, err)
	}
}

func TestSystematicReturnsPartialReportOnRunnerError(t *testing.T) {
	failure := errors.New("runner failed")
	calls := 0
	report, err := Systematic(func(prefix []int) (Execution, error) {
		calls++
		if calls == 2 {
			return Execution{Outcome: "invalid", Failed: true}, failure
		}
		return Execution{
			Decisions: []Decision{{Key: "x", Reader: 1, Writer: 2, Choice: 1, Alternatives: 2}},
			Outcome:   "assertion", Failed: true,
		}, nil
	}, 3, 1)
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want runner error", err)
	}
	if calls != 2 || report.Runs != 2 || report.Evaluated != 1 || report.Failures != 1 || report.FirstFailure != 1 || report.Complete {
		t.Fatalf("unexpected partial report: %+v, calls = %d", report, calls)
	}
	if len(report.Corpus) != 1 || !reflect.DeepEqual(report.Outcomes, map[string]int{"assertion": 1}) {
		t.Fatalf("runner error was recorded as an outcome: %+v", report)
	}
}

func TestSystematicRejectsInvalidExecutions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		maxDecisions  int
		invalid       Execution
		afterFirstRun bool
	}{
		{"decision bound", 0, Execution{Decisions: []Decision{{Key: "x", Reader: 1, Writer: 2, Choice: 1, Alternatives: 2}}}, false},
		{"non-newest tail", 1, Execution{Decisions: []Decision{{Key: "x", Reader: 1, Writer: 2, Choice: 0, Alternatives: 2}}}, false},
		{"invalid metadata", 1, Execution{Decisions: []Decision{{Key: "x", Reader: 1, Writer: 0, Choice: 0, Alternatives: 1}}}, false},
		{"divergent prefix", 1, Execution{Decisions: []Decision{{Key: "x", Reader: 1, Writer: 2, Choice: 1, Alternatives: 2}}}, true},
		{"short prefix", 1, Execution{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			report, err := Systematic(func(prefix []int) (Execution, error) {
				calls++
				if tc.afterFirstRun && calls == 1 {
					return Execution{Decisions: []Decision{{Key: "x", Reader: 1, Writer: 2, Choice: 1, Alternatives: 2}}, Outcome: "valid"}, nil
				}
				return tc.invalid, nil
			}, 3, tc.maxDecisions)
			wantEvaluated := 0
			if tc.afterFirstRun {
				wantEvaluated = 1
			}
			if err == nil || report.Complete || calls != wantEvaluated+1 || report.Runs != calls || report.Evaluated != wantEvaluated || report.Failures != 0 {
				t.Fatalf("invalid execution report = %+v, error = %v, calls = %d", report, err, calls)
			}
			if report.Outcomes["valid"] != wantEvaluated {
				t.Fatalf("lost valid outcomes: %+v", report)
			}
		})
	}
}

func TestSystematicRejectsInvalidBoundsAndRunner(t *testing.T) {
	calls := 0
	run := func(prefix []int) (Execution, error) {
		calls++
		return Execution{}, nil
	}
	for _, tc := range []struct {
		name         string
		run          Runner
		maxRuns      int
		maxDecisions int
	}{
		{"nil runner", nil, 1, 1},
		{"zero runs", run, 0, 1},
		{"negative runs", run, -1, 1},
		{"negative decisions", run, 1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := Systematic(tc.run, tc.maxRuns, tc.maxDecisions)
			if err == nil || report.Runs != 0 || report.Evaluated != 0 || report.Complete || report.Outcomes == nil {
				t.Fatalf("invalid configuration report = %+v, error = %v", report, err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("runner called %d times for invalid configuration", calls)
	}
}

func TestSystematicRunBoundDoesNotMaterializeWideFrontier(t *testing.T) {
	calls := 0
	report, err := Systematic(func([]int) (Execution, error) {
		calls++
		return Execution{Outcome: "newest", Decisions: []Decision{{
			Key: "x", Reader: 1, Writer: 2,
			Choice: (1 << 30) - 1, Alternatives: 1 << 30,
		}}}, nil
	}, 1, 1)
	if err != nil || report.Complete || calls != 1 || report.Evaluated != 1 || report.Outcomes["newest"] != 1 {
		t.Fatalf("wide bounded search: %+v error=%v calls=%d", report, err, calls)
	}
}
