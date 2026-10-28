// Package exploration searches admitted read-choice traces of deterministic workloads.
// The workload owns execution and oracle admission; search never relaxes legality.
package exploration

import (
	"fmt"
	"time"
)

// Decision describes one external read and its ordered admitted candidates.
// Reader and Writer are session identities; initial values have Writer zero.
// Key identifies a variable, not its returned value.
type Decision struct {
	Key          string `json:"key"`
	Reader       int    `json:"reader"`
	Writer       int    `json:"writer"`
	Initial      bool   `json:"initial"`
	Choice       int    `json:"choice"`
	Alternatives int    `json:"alternatives"`
}

// TransactionCounts distinguishes successful finishes from rejected attempts.
// Completed includes explicit rollbacks, never rejected or interrupted attempts.
type TransactionCounts struct {
	Attempted  int `json:"attempted"`
	Committed  int `json:"committed"`
	RolledBack int `json:"rolled_back"`
	Rejected   int `json:"rejected"`
	Completed  int `json:"completed"`
}

func (counts *TransactionCounts) Add(other TransactionCounts) {
	counts.Attempted += other.Attempted
	counts.Committed += other.Committed
	counts.RolledBack += other.RolledBack
	counts.Rejected += other.Rejected
	counts.Completed += other.Completed
}

// Execution owns its decision snapshot and a canonical client-observable outcome.
// Failed marks an application assertion failure, not an execution/checker error.
type Execution struct {
	Decisions    []Decision        `json:"decisions"`
	Outcome      string            `json:"outcome"`
	Failed       bool              `json:"failed"`
	Transactions TransactionCounts `json:"transactions"`
	Elapsed      time.Duration     `json:"workload_elapsed_ns"`
}

// Runner starts a fresh deterministic workload, replays the supplied prefix,
// then chooses newest admitted candidates. It must not mutate the prefix.
// Every external read is reported, including singleton choices. Non-read
// nondeterminism must be fixed by the caller; SQL/client errors are returned.
type Runner func(prefix []int) (Execution, error)

// Report keeps novel traces for replay; Complete is only an exhaustive verdict.
type Report struct {
	Seed         uint64            `json:"seed"`
	Runs         int               `json:"runs"`
	Evaluated    int               `json:"evaluated"`
	Failures     int               `json:"failures"`
	FirstFailure int               `json:"first_failure"`
	FeatureCount int               `json:"feature_count"`
	Outcomes     map[string]int    `json:"outcomes"`
	Corpus       []Execution       `json:"corpus"`
	Complete     bool              `json:"complete"`
	Transactions TransactionCounts `json:"transactions"`
	Elapsed      time.Duration     `json:"workload_elapsed_ns"`
}

// Feature uses first-appearance roles rather than raw identities or key names.
// It records a read-from edge and the preceding source/variable for that reader.
// This is a coverage abstraction, not a proof that traces are equivalent.
type Feature struct {
	Reader           int  `json:"reader"`
	Writer           int  `json:"writer"`
	Variable         int  `json:"variable"`
	PreviousWriter   int  `json:"previous_writer"`
	PreviousVariable int  `json:"previous_variable"`
	Initial          bool `json:"initial"`
	Newest           bool `json:"newest"`
}

// SemanticFeatures preserves identity relationships without rewarding fresh IDs.
func SemanticFeatures(execution Execution) []Feature {
	roles := map[int]int{0: 0}
	role := func(id int) int {
		if value, ok := roles[id]; ok {
			return value
		}
		value := len(roles)
		roles[id] = value
		return value
	}
	variables := make(map[string]int)
	previous := make(map[int]Feature)
	features := make([]Feature, 0, len(execution.Decisions))
	for _, decision := range execution.Decisions {
		reader, writer := role(decision.Reader), role(decision.Writer)
		variable, ok := variables[decision.Key]
		if !ok {
			variable = len(variables) + 1
			variables[decision.Key] = variable
		}
		prior := previous[reader]
		feature := Feature{Reader: reader, Writer: writer, Variable: variable,
			PreviousWriter: prior.Writer, PreviousVariable: prior.Variable,
			Initial: decision.Initial, Newest: decision.Choice == decision.Alternatives-1}
		features = append(features, feature)
		previous[reader] = feature
	}
	return features
}

func validate(run Runner, maxRuns, maxDecisions int) error {
	if run == nil || maxRuns <= 0 || maxDecisions < 0 {
		return fmt.Errorf("runner required, run bound must be positive, decision bound must be nonnegative")
	}
	return nil
}

func replay(run Runner, prefix []int, maxDecisions int) (Execution, error) {
	execution, err := run(prefix)
	if err != nil {
		return execution, err
	}
	if len(execution.Decisions) > maxDecisions {
		return execution, fmt.Errorf("execution exceeds decision bound %d", maxDecisions)
	}
	if len(execution.Decisions) < len(prefix) {
		return execution, fmt.Errorf("execution ended before replay prefix")
	}
	for i, decision := range execution.Decisions {
		if decision.Key == "" || decision.Reader <= 0 || decision.Writer < 0 || decision.Initial != (decision.Writer == 0) || decision.Alternatives <= 0 || decision.Choice < 0 || decision.Choice >= decision.Alternatives {
			return execution, fmt.Errorf("invalid admitted decision at index %d", i)
		}
		if i < len(prefix) && decision.Choice != prefix[i] {
			return execution, fmt.Errorf("replay diverged at decision %d", i)
		}
		if i >= len(prefix) && decision.Choice != decision.Alternatives-1 {
			return execution, fmt.Errorf("unfixed decision %d did not choose newest admitted", i)
		}
	}
	return execution, nil
}

type outcomeKey struct {
	value  string
	failed bool
}

func record(report *Report, execution Execution, features map[Feature]bool, outcomes map[outcomeKey]bool) {
	report.Evaluated++
	report.Transactions.Add(execution.Transactions)
	report.Elapsed += execution.Elapsed
	if execution.Failed {
		report.Failures++
		if report.FirstFailure == 0 {
			report.FirstFailure = report.Runs
		}
	}
	key := outcomeKey{execution.Outcome, execution.Failed}
	novel := !outcomes[key]
	outcomes[key] = true
	report.Outcomes[execution.Outcome]++
	for _, feature := range SemanticFeatures(execution) {
		if !features[feature] {
			novel = true
			features[feature] = true
		}
	}
	report.FeatureCount = len(features)
	if novel {
		report.Corpus = append(report.Corpus, execution)
	}
}
