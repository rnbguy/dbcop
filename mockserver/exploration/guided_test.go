package exploration

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func guidedBranchingRun(prefix []int) (Execution, error) {
	choice := func(index, alternatives int) int {
		if index < len(prefix) {
			return prefix[index]
		}
		return alternatives - 1
	}
	root := choice(0, 2)
	execution := Execution{Decisions: []Decision{{
		Key: "gate", Reader: 1, Writer: root + 2, Choice: root, Alternatives: 2,
	}}}
	if root == 0 {
		execution.Outcome = "closed"
		return execution, nil
	}
	value := choice(1, 3)
	execution.Decisions = append(execution.Decisions, Decision{
		Key: "value", Reader: 1, Writer: value + 2, Choice: value, Alternatives: 3,
	})
	execution.Outcome = fmt.Sprintf("value=%d", value)
	execution.Failed = value == 0
	return execution, nil
}

func TestGuidedBranchesAndReplays(t *testing.T) {
	var prefixes [][]int
	var executions []Execution
	run := func(prefix []int) (Execution, error) {
		prefixes = append(prefixes, append([]int(nil), prefix...))
		execution, err := guidedBranchingRun(prefix)
		executions = append(executions, execution)
		return execution, err
	}
	report, err := Guided(run, 256, 2, 17)
	if err != nil {
		t.Fatal(err)
	}
	if report.Seed != 17 || report.Runs != 256 || report.Evaluated != 256 || report.Complete {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(report.Outcomes) != 4 || len(report.Corpus) != 4 || report.Failures == 0 || report.FirstFailure == 0 {
		t.Fatalf("missed branching outcomes or failure: %+v", report)
	}
	if len(prefixes[0]) != 0 || executions[0].Outcome != "value=2" {
		t.Fatalf("first run did not use newest tail: %v %+v", prefixes[0], executions[0])
	}
	for i, prefix := range prefixes[1:] {
		if len(prefix) == 0 {
			t.Fatal("mutation used empty prefix")
		}
		changed := len(prefix) - 1
		found := false
		for _, prior := range executions[:i+1] {
			if len(prior.Decisions) <= changed || prior.Decisions[changed].Choice == prefix[changed] {
				continue
			}
			matches := true
			for j := range changed {
				matches = matches && prior.Decisions[j].Choice == prefix[j]
			}
			found = found || matches
		}
		if !found {
			t.Fatalf("prefix %v does not mutate one prior decision", prefix)
		}
	}
	for _, retained := range report.Corpus {
		prefix := make([]int, len(retained.Decisions))
		for i, decision := range retained.Decisions {
			prefix[i] = decision.Choice
		}
		replayed, err := replay(guidedBranchingRun, prefix, 2)
		if err != nil || !reflect.DeepEqual(replayed, retained) {
			t.Fatalf("retained trace is not replayable: %+v, %v", replayed, err)
		}
	}
	again, err := Guided(guidedBranchingRun, 256, 2, 17)
	if err != nil || !reflect.DeepEqual(report, again) {
		t.Fatalf("same seed changed search: %+v, %v", again, err)
	}
}

func TestGuidedRenamingDoesNotRetainNovelCases(t *testing.T) {
	calls := 0
	run := func(prefix []int) (Execution, error) {
		calls++
		choice := 1
		if len(prefix) != 0 {
			choice = prefix[0]
		}
		return Execution{Outcome: "same", Decisions: []Decision{{
			Key: fmt.Sprintf("key-%d", calls), Reader: calls*10 + 1, Writer: calls*10 + 2,
			Choice: choice, Alternatives: 2,
		}}}, nil
	}
	report, err := Guided(run, 60, 1, 41)
	if err != nil {
		t.Fatal(err)
	}
	if report.Runs != 60 || report.FeatureCount != 2 || len(report.Corpus) != 2 || report.Outcomes["same"] != 60 {
		t.Fatalf("renaming inflated coverage: %+v", report)
	}
}

func TestGuidedStopsWithoutMutableCases(t *testing.T) {
	for _, decisions := range [][]Decision{nil, {{Key: "x", Reader: 1, Initial: true, Alternatives: 1}}} {
		run := func(prefix []int) (Execution, error) {
			return Execution{Decisions: decisions, Outcome: "terminal"}, nil
		}
		report, err := Guided(run, 20, 1, 9)
		if err != nil || report.Runs != 1 || report.Evaluated != 1 || len(report.Corpus) != 1 || report.Complete {
			t.Fatalf("terminal search: %+v, %v", report, err)
		}
	}
}

func TestGuidedInvalidArgumentsRetainSeed(t *testing.T) {
	for _, test := range []struct {
		run             Runner
		runs, decisions int
	}{
		{nil, 1, 1},
		{guidedBranchingRun, 0, 1},
		{guidedBranchingRun, -1, 1},
		{guidedBranchingRun, 1, -1},
	} {
		report, err := Guided(test.run, test.runs, test.decisions, 99)
		if err == nil || report.Seed != 99 || report.Runs != 0 || report.Evaluated != 0 || report.Complete {
			t.Fatalf("invalid arguments: %+v, %v", report, err)
		}
	}
}

func TestGuidedPropagatesRunnerAndReplayErrors(t *testing.T) {
	sentinel := errors.New("runner failed")
	for _, test := range []struct {
		name                   string
		run                    Runner
		bound, runs, evaluated int
		cause                  error
	}{
		{"runner", func([]int) (Execution, error) { return Execution{}, sentinel }, 2, 1, 0, sentinel},
		{"metadata", func([]int) (Execution, error) {
			return Execution{Decisions: []Decision{{Key: "x", Reader: 1, Writer: 2, Choice: 2, Alternatives: 2}}}, nil
		}, 2, 1, 0, nil},
		{"bound", guidedBranchingRun, 1, 1, 0, nil},
		{"newest", func([]int) (Execution, error) {
			return Execution{Decisions: []Decision{{Key: "x", Reader: 1, Initial: true, Choice: 0, Alternatives: 2}}}, nil
		}, 2, 1, 0, nil},
		{"divergent", func([]int) (Execution, error) { return guidedBranchingRun(nil) }, 2, 2, 1, nil},
		{"partial", func(prefix []int) (Execution, error) {
			if len(prefix) > 0 {
				return Execution{}, sentinel
			}
			return guidedBranchingRun(prefix)
		}, 2, 2, 1, sentinel},
	} {
		t.Run(test.name, func(t *testing.T) {
			report, err := Guided(test.run, 8, test.bound, 71)
			if err == nil || (test.cause != nil && !errors.Is(err, test.cause)) {
				t.Fatalf("expected replay error, got %v", err)
			}
			if report.Seed != 71 || report.Runs != test.runs || report.Evaluated != test.evaluated || report.Complete {
				t.Fatalf("incorrect partial report: %+v", report)
			}
			if len(report.Corpus) != test.evaluated {
				t.Fatalf("invalid execution retained: %+v", report.Corpus)
			}
		})
	}
}
