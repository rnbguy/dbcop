package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/rnbguy/dbcop/mockserver/exploration"
)

// SQLExplorationOptions bounds a fixed workload, not a concurrent client schedule.
// MaxTransactions rejects larger workloads; it never truncates the fixture or SQL.
type SQLExplorationOptions struct {
	Mode            string `json:"mode"`
	Seed            uint64 `json:"seed"`
	MaxRuns         int    `json:"max_runs"`
	MaxDecisions    int    `json:"max_decisions"`
	MaxTransactions int    `json:"max_transactions"`
}

func (options SQLExplorationOptions) validate(workload SQLWorkload) error {
	if options.Mode != "guided" && options.Mode != "systematic" {
		return fmt.Errorf("exploration mode must be guided or systematic")
	}
	if options.MaxRuns <= 0 || options.MaxDecisions < 0 || options.MaxTransactions < 0 {
		return fmt.Errorf("run bound must be positive; decision and transaction bounds must be nonnegative")
	}
	if len(workload.Transactions) > options.MaxTransactions {
		return fmt.Errorf("workload exceeds transaction bound %d", options.MaxTransactions)
	}
	return nil
}

type SQLInterruptedExecution struct {
	Prefix       []int                         `json:"prefix"`
	Choices      []ReplayChoice                `json:"choices"`
	Transactions exploration.TransactionCounts `json:"transactions"`
	Elapsed      time.Duration                 `json:"workload_elapsed_ns"`
}

// SQLExplorationArtifact retains every evaluated run, including passing cases.
// Report.Corpus is the coverage-selected mutation corpus. Interrupted, when set,
// describes an execution error, not an application assertion failure.
type SQLExplorationArtifact struct {
	SchemaVersion    int                      `json:"schema_version"`
	ValidationPolicy string                   `json:"validation_policy"`
	Workload         SQLWorkload              `json:"workload"`
	Options          SQLExplorationOptions    `json:"options"`
	Report           exploration.Report       `json:"report"`
	Executions       []SQLResult              `json:"executions"`
	Error            string                   `json:"execution_error,omitempty"`
	Interrupted      *SQLInterruptedExecution `json:"interrupted,omitempty"`
}

type sqlWorkloadRunner struct {
	workload SQLWorkload
	branch   sqlReplayBranch
	limit    *int
	last     SQLResult
}

// Freeze the complete input once; callers cannot change SQL or seeds mid-search.
func newSQLWorkloadRunner(workload SQLWorkload, limit *int) (*sqlWorkloadRunner, error) {
	encoded, err := json.Marshal(workload)
	if err != nil {
		return nil, err
	}
	frozen, err := DecodeSQLWorkload(bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	return &sqlWorkloadRunner{workload: frozen, limit: limit}, nil
}

func (runner *sqlWorkloadRunner) run(prefix []int) (exploration.Execution, error) {
	if prefix == nil {
		prefix = []int{}
	}
	result, err := runSQLWorkload(runner.workload, &sqlReplayController{
		prefix: prefix, prefixMode: true, branch: &runner.branch, decisionLimit: runner.limit,
	})
	runner.last = result
	if err != nil {
		execution, _ := sqlExplorationExecution(result)
		execution.Outcome = ""
		return execution, err
	}
	return sqlExplorationExecution(result)
}

func sqlExplorationExecution(result SQLResult) (exploration.Execution, error) {
	execution := exploration.Execution{Failed: result.Failure != nil,
		Transactions: result.Transactions, Elapsed: result.Elapsed,
		Decisions: make([]exploration.Decision, 0, len(result.Choices))}
	writers := make(map[string]int, len(result.Workload.Transactions))
	for _, transaction := range result.Workload.Transactions {
		writers[transaction.ID] = transaction.SessionID
	}
	for _, choice := range result.Choices {
		execution.Decisions = append(execution.Decisions, exploration.Decision{
			Key: choice.Key, Reader: choice.Reader, Writer: writers[choice.Selected.TransactionID],
			Initial: choice.Selected.Initial, Choice: choice.Choice, Alternatives: len(choice.Candidates),
		})
	}
	// History is evidence, not a novel application outcome or a solver witness.
	outcome, err := json.Marshal(result.ObservedRows)
	execution.Outcome = string(outcome)
	return execution, err
}

// SQLWorkloadRunner reuses stable SQL source identities and checks the ordered
// admitted universe whenever the same source prefix is visited again.
func SQLWorkloadRunner(workload SQLWorkload) exploration.Runner {
	runner, err := newSQLWorkloadRunner(workload, nil)
	return func(prefix []int) (exploration.Execution, error) {
		if err != nil {
			return exploration.Execution{}, err
		}
		return runner.run(prefix)
	}
}

// ExploreSQLWorkload executes each supplied prefix in a fresh native store,
// then chooses newest admitted sources. SQL errors stop search, never count as
// assertion failures, and return a partial artifact with the exact error prefix.
func ExploreSQLWorkload(workload SQLWorkload, options SQLExplorationOptions) (SQLExplorationArtifact, error) {
	runner, err := newSQLWorkloadRunner(workload, &options.MaxDecisions)
	if err != nil {
		return SQLExplorationArtifact{}, err
	}
	if err := options.validate(runner.workload); err != nil {
		return SQLExplorationArtifact{}, err
	}
	artifact := SQLExplorationArtifact{SchemaVersion: sqlReplaySchema, ValidationPolicy: sqlValidationPolicy,
		Workload: runner.workload, Options: options, Executions: make([]SQLResult, 0)}
	run := func(prefix []int) (exploration.Execution, error) {
		execution, err := runner.run(prefix)
		if err != nil {
			artifact.Error = err.Error()
			choices := runner.last.Choices
			if choices == nil {
				choices = []ReplayChoice{}
			}
			artifact.Interrupted = &SQLInterruptedExecution{Prefix: append([]int{}, prefix...), Choices: choices, Transactions: runner.last.Transactions, Elapsed: runner.last.Elapsed}
		} else {
			artifact.Executions = append(artifact.Executions, runner.last)
		}
		return execution, err
	}
	if options.Mode == "guided" {
		artifact.Report, err = exploration.Guided(run, options.MaxRuns, options.MaxDecisions, options.Seed)
	} else {
		artifact.Report, err = exploration.Systematic(run, options.MaxRuns, options.MaxDecisions)
		artifact.Report.Seed = options.Seed
	}
	if artifact.Report.Corpus == nil {
		artifact.Report.Corpus = []exploration.Execution{}
	}
	return artifact, err
}

func sqlResultArtifact(result SQLResult) ReplayArtifact {
	artifact := ReplayArtifact{SchemaVersion: sqlReplaySchema, Revision: result.Revision,
		ValidationPolicy: sqlValidationPolicy, Workload: result.Workload, Choices: result.Choices,
		ObservedRows: result.ObservedRows, History: result.History, Variables: result.Variables}
	if result.Failure != nil {
		artifact.Failure = *result.Failure
	}
	return artifact
}

// DecodeSQLExplorationArtifact rejects malformed evidence before running SQL.
func DecodeSQLExplorationArtifact(reader io.Reader) (SQLExplorationArtifact, error) {
	var artifact SQLExplorationArtifact
	if err := decodeSQLJSON(reader, &artifact); err != nil {
		return artifact, err
	}
	return artifact, validateSQLExplorationArtifact(artifact)
}

func validateSQLExplorationArtifact(artifact SQLExplorationArtifact) error {
	if artifact.SchemaVersion != sqlReplaySchema || artifact.ValidationPolicy != sqlValidationPolicy || artifact.Executions == nil {
		return fmt.Errorf("invalid SQL exploration artifact: schema, policy or executions")
	}
	normalized, err := normalizeSQLWorkload(artifact.Workload)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(normalized, artifact.Workload) {
		return fmt.Errorf("invalid SQL exploration artifact: unnormalized workload")
	}
	if err := artifact.Options.validate(artifact.Workload); err != nil {
		return err
	}
	report := artifact.Report
	if report.Seed != artifact.Options.Seed || report.Runs <= 0 || report.Runs > artifact.Options.MaxRuns || report.Evaluated != len(artifact.Executions) || report.Evaluated > report.Runs || report.Failures < 0 || report.Failures > report.Evaluated || report.Elapsed < 0 || report.Outcomes == nil || report.Corpus == nil {
		return fmt.Errorf("invalid SQL exploration artifact: search accounting")
	}
	if artifact.Error == "" && (report.Runs != report.Evaluated || artifact.Interrupted != nil) || artifact.Error != "" && (report.Runs != report.Evaluated+1 || report.Complete || artifact.Interrupted == nil) || report.Complete && artifact.Options.Mode != "systematic" {
		return fmt.Errorf("invalid SQL exploration artifact: completion or execution-error status")
	}
	var counts exploration.TransactionCounts
	var elapsed time.Duration
	features := make(map[exploration.Feature]bool)
	outcomes := make(map[string]int)
	type outcomeKey struct {
		value  string
		failed bool
	}
	seenOutcomes := make(map[outcomeKey]bool)
	corpus := make([]exploration.Execution, 0)
	var tree sqlReplayBranch
	failures, firstFailure := 0, 0
	commits, rollbacks := 0, 0
	for _, transaction := range artifact.Workload.Transactions {
		if transaction.Finish == "commit" {
			commits++
		} else {
			rollbacks++
		}
	}
	for index, result := range artifact.Executions {
		if !reflect.DeepEqual(result.Workload, artifact.Workload) || len(result.Choices) > artifact.Options.MaxDecisions || result.Elapsed < 0 {
			return fmt.Errorf("invalid SQL exploration artifact: changed workload or execution bound")
		}
		progress := result.Transactions
		if progress.Attempted != commits+rollbacks || progress.Completed != progress.Attempted || progress.Committed != commits || progress.RolledBack != rollbacks || progress.Rejected != 0 {
			return fmt.Errorf("invalid SQL exploration artifact: completed transaction accounting")
		}
		if result.Failure != nil && (result.Failure.AssertionID != artifact.Workload.Assertion.ID || result.Failure.Kind != "rows_mismatch") {
			return fmt.Errorf("invalid SQL exploration artifact: malformed assertion failure")
		}
		if err := validateSQLArtifactEvidence(sqlResultArtifact(result), false); err != nil {
			return err
		}
		branch := &tree
		for decision := range result.Choices {
			choice := &result.Choices[decision]
			if branch.terminal || branch.choice != nil && !sameSQLUniverse(*choice, *branch.choice) {
				return fmt.Errorf("invalid SQL exploration artifact: admitted universe changed for a source prefix")
			}
			if branch.choice == nil {
				branch.choice = choice
				branch.children = make(map[int]*sqlReplayBranch)
			}
			next := branch.children[choice.Choice]
			if next == nil {
				next = &sqlReplayBranch{}
				branch.children[choice.Choice] = next
			}
			branch = next
		}
		if branch.choice != nil {
			return fmt.Errorf("invalid SQL exploration artifact: missing reads for a source prefix")
		}
		branch.terminal = true
		counts.Add(progress)
		elapsed += result.Elapsed
		execution, err := sqlExplorationExecution(result)
		if err != nil {
			return err
		}
		key := outcomeKey{execution.Outcome, execution.Failed}
		novel := !seenOutcomes[key]
		seenOutcomes[key] = true
		outcomes[execution.Outcome]++
		for _, feature := range exploration.SemanticFeatures(execution) {
			if !features[feature] {
				novel = true
				features[feature] = true
			}
		}
		if novel {
			corpus = append(corpus, execution)
		}
		if execution.Failed {
			failures++
			if firstFailure == 0 {
				firstFailure = index + 1
			}
		}
	}
	if interrupted := artifact.Interrupted; interrupted != nil {
		progress := interrupted.Transactions
		if interrupted.Prefix == nil || len(interrupted.Prefix) > artifact.Options.MaxDecisions || interrupted.Elapsed < 0 || progress.Attempted < 0 || progress.Attempted > len(artifact.Workload.Transactions) || progress.Committed < 0 || progress.RolledBack < 0 || progress.Rejected < 0 || progress.Rejected > 1 || progress.Completed != progress.Committed+progress.RolledBack || progress.Completed+progress.Rejected > progress.Attempted {
			return fmt.Errorf("invalid SQL exploration artifact: interrupted transaction accounting")
		}
		if interrupted.Choices == nil || len(interrupted.Choices) > artifact.Options.MaxDecisions {
			return fmt.Errorf("invalid SQL exploration artifact: interrupted read decision bound")
		}
		for _, choice := range interrupted.Prefix {
			if choice < 0 {
				return fmt.Errorf("invalid SQL exploration artifact: negative error prefix choice")
			}
		}
		counts.Add(progress)
		elapsed += interrupted.Elapsed
	}
	if report.Transactions != counts || report.Elapsed != elapsed || report.FeatureCount != len(features) || report.Failures != failures || report.FirstFailure != firstFailure || !reflect.DeepEqual(report.Outcomes, outcomes) || !reflect.DeepEqual(report.Corpus, corpus) {
		return fmt.Errorf("invalid SQL exploration artifact: coverage or aggregate accounting changed")
	}
	if report.Complete && !completeSQLChoiceTree(&tree) {
		return fmt.Errorf("invalid SQL exploration artifact: unexplored admitted source branch")
	}
	return nil
}

func completeSQLChoiceTree(branch *sqlReplayBranch) bool {
	if branch.choice == nil {
		return branch.terminal
	}
	if len(branch.children) != len(branch.choice.Candidates) {
		return false
	}
	for _, child := range branch.children {
		if !completeSQLChoiceTree(child) {
			return false
		}
	}
	return true
}

// ReplaySQLExplorationArtifact strictly reproduces every evaluated case, whether
// it passed or failed. Timing and process-local version numbers are diagnostic.
// An interrupted search remains an error artifact, never a complete verdict.
func ReplaySQLExplorationArtifact(artifact SQLExplorationArtifact) ([]SQLResult, error) {
	if err := validateSQLExplorationArtifact(artifact); err != nil {
		return nil, err
	}
	results := make([]SQLResult, 0, len(artifact.Executions))
	for index, expected := range artifact.Executions {
		result, err := runSQLWorkload(artifact.Workload, &sqlReplayController{strict: true, expected: expected.Choices, decisionLimit: &artifact.Options.MaxDecisions})
		if err != nil {
			return results, fmt.Errorf("replay corpus case %d: %w", index, err)
		}
		if !reflect.DeepEqual(result.Failure, expected.Failure) || !reflect.DeepEqual(result.ObservedRows, expected.ObservedRows) || !bytes.Equal(result.History, expected.History) || !reflect.DeepEqual(result.Variables, expected.Variables) || result.Transactions != expected.Transactions {
			return results, fmt.Errorf("replay divergence: corpus case %d outcome, history or transaction counts changed", index)
		}
		results = append(results, result)
	}
	if artifact.Error != "" {
		result, err := runSQLWorkload(artifact.Workload, &sqlReplayController{prefix: artifact.Interrupted.Prefix, prefixMode: true, decisionLimit: &artifact.Options.MaxDecisions})
		if err == nil || err.Error() != artifact.Error {
			return results, fmt.Errorf("replay divergence: interrupted execution changed: got %v; want %s", err, artifact.Error)
		}
		if result.Transactions != artifact.Interrupted.Transactions || len(result.Choices) != len(artifact.Interrupted.Choices) {
			return results, fmt.Errorf("replay divergence: interrupted transaction or read counts changed")
		}
		for index, expected := range artifact.Interrupted.Choices {
			actual := result.Choices[index]
			if !sameSQLUniverse(actual, expected) || actual.Choice != expected.Choice || actual.Selected != expected.Selected {
				return results, fmt.Errorf("replay divergence: interrupted admitted sources changed at decision %d", index)
			}
		}
		return results, fmt.Errorf("reproduced execution error: %s", artifact.Error)
	}
	return results, nil
}
