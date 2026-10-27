// Command replay explores, captures, strictly replays, and reduces fixed SQL.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rnbguy/dbcop/mockserver/backend"
	"github.com/rnbguy/dbcop/mockserver/exploration"
)

type commandResult struct {
	Action               string                         `json:"action"`
	Status               string                         `json:"status"`
	Seed                 *uint64                        `json:"seed,omitempty"`
	ReadPolicy           string                         `json:"read_policy,omitempty"`
	DeviationProbability *float64                       `json:"deviation_probability,omitempty"`
	DeviationBudget      *int                           `json:"deviation_budget,omitempty"`
	Failure              *backend.FailureFingerprint    `json:"failure,omitempty"`
	ObservedRows         []backend.TypedRow             `json:"observed_rows,omitempty"`
	Artifact             string                         `json:"artifact,omitempty"`
	Trials               *int                           `json:"trials,omitempty"`
	ReductionStatus      string                         `json:"reduction_status,omitempty"`
	Error                string                         `json:"error,omitempty"`
	Exploration          *exploration.Report            `json:"exploration,omitempty"`
	Transactions         *exploration.TransactionCounts `json:"transactions,omitempty"`
	WorkloadElapsed      time.Duration                  `json:"workload_elapsed_ns,omitempty"`
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	result, code, err := execute(args, stderr)
	if err != nil {
		result.Status, result.Error = "error", err.Error()
		fmt.Fprintln(stderr, "replay:", err)
		code = 2
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "replay: write result:", err)
		return 2
	}
	return code
}

func execute(args []string, stderr io.Writer) (result commandResult, code int, err error) {
	if len(args) == 0 {
		return result, 2, fmt.Errorf("expected run, replay, minimize, explore, or replay-corpus subcommand")
	}
	result.Action = args[0]
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	var workloadPath, artifactPath, outputPath string
	maxTrials := 128
	options := backend.SQLExplorationOptions{}
	switch args[0] {
	case "explore":
		flags.StringVar(&workloadPath, "workload", "", "fixed-order SQL workload JSON with complete fixture")
		flags.StringVar(&artifactPath, "artifact", "", "new complete SQL exploration corpus path")
		flags.StringVar(&options.Mode, "mode", "guided", "guided or systematic admitted-source exploration")
		flags.Uint64Var(&options.Seed, "seed", 1, "deterministic corpus mutation seed; does not reproduce concurrent JDBC schedules")
		flags.IntVar(&options.MaxRuns, "max-runs", 128, "positive fresh-store execution bound")
		flags.IntVar(&options.MaxDecisions, "max-decisions", 4096, "nonnegative external read bound per execution, including singleton choices")
		flags.IntVar(&options.MaxTransactions, "max-transactions", 1000, "fixed workload transaction bound; oversized workloads fail, never truncate")
	case "replay-corpus":
		flags.StringVar(&artifactPath, "artifact", "", "complete SQL exploration corpus JSON")
	case "run":
		flags.StringVar(&workloadPath, "workload", "", "controlled SQL workload JSON")
		flags.StringVar(&artifactPath, "artifact", "", "new failure artifact path")
	case "replay":
		flags.StringVar(&artifactPath, "artifact", "", "failure artifact JSON")
	case "minimize":
		flags.StringVar(&artifactPath, "artifact", "", "failure artifact JSON")
		flags.StringVar(&outputPath, "output", "", "new minimized artifact path")
		flags.IntVar(&maxTrials, "max-trials", 128, "positive candidate execution budget")
	default:
		return result, 2, fmt.Errorf("unknown subcommand %q", args[0])
	}
	if err = flags.Parse(args[1:]); err != nil {
		return result, 2, err
	}
	if flags.NArg() != 0 {
		return result, 2, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if artifactPath == "" || ((args[0] == "run" || args[0] == "explore") && workloadPath == "") || (args[0] == "minimize" && outputPath == "") {
		return result, 2, fmt.Errorf("required file flags are missing")
	}
	if args[0] == "explore" {
		workload, err := decodeFile(workloadPath, backend.DecodeSQLWorkload)
		if err != nil {
			return result, 2, err
		}
		artifact, explorationErr := backend.ExploreSQLWorkload(workload, options)
		if artifact.SchemaVersion == 0 {
			return result, 2, explorationErr
		}
		if err := persistArtifact(artifactPath, artifact); err != nil {
			return result, 2, errors.Join(explorationErr, err)
		}
		result.Artifact, result.Exploration = artifactPath, &artifact.Report
		result.Seed, result.ReadPolicy = artifact.Workload.Seed, "replay-prefix-then-newest-admitted"
		if explorationErr != nil {
			return result, 2, explorationErr
		}
		result.Status = "explored"
		if artifact.Report.Failures != 0 {
			result.Status = "assertion_failures"
			return result, 1, nil
		}
		return result, 0, nil
	}
	if args[0] == "replay-corpus" {
		artifact, err := decodeFile(artifactPath, backend.DecodeSQLExplorationArtifact)
		if err != nil {
			return result, 2, err
		}
		executions, err := backend.ReplaySQLExplorationArtifact(artifact)
		result.Artifact, result.Exploration = artifactPath, &artifact.Report
		if err != nil {
			return result, 2, err
		}
		if len(executions) != 0 {
			reportRevisionDrift(stderr, artifact.Executions[0].Revision, executions[0].Revision)
		}
		result.Status = "corpus_reproduced"
		return result, 0, nil
	}
	if args[0] == "run" {
		workload, err := decodeFile(workloadPath, backend.DecodeSQLWorkload)
		if err != nil {
			return result, 2, err
		}
		execution, err := backend.RunSQLWorkload(workload, nil)
		result.Transactions, result.WorkloadElapsed = &execution.Transactions, execution.Elapsed
		budget := 1
		if execution.Workload.DeviationBudget != nil {
			budget = *execution.Workload.DeviationBudget
		}
		result.DeviationBudget = &budget
		result.Seed, result.ReadPolicy = execution.Workload.Seed, execution.Workload.ReadPolicy
		result.DeviationProbability = execution.Workload.DeviationProbability
		if err != nil {
			return result, 2, err
		}
		result.ObservedRows = execution.ObservedRows
		if execution.Failure == nil {
			result.Status = "passed"
			return result, 0, nil
		}
		artifact, err := backend.CaptureSQLFailure(execution.Workload, execution)
		if err != nil {
			return result, 2, err
		}
		if err = persistArtifact(artifactPath, artifact); err != nil {
			return result, 2, err
		}
		result.Status, result.Failure, result.Artifact = "rows_mismatch", execution.Failure, artifactPath
		return result, 1, nil
	}
	artifact, err := decodeFile(artifactPath, backend.DecodeReplayArtifact)
	if err != nil {
		return result, 2, err
	}
	if args[0] == "replay" {
		execution, err := backend.ReplaySQLArtifact(artifact)
		if err != nil {
			return result, 2, err
		}
		reportRevisionDrift(stderr, artifact.Revision, execution.Revision)
		result.Status, result.Failure, result.ObservedRows = "reproduced", execution.Failure, execution.ObservedRows
		result.Artifact = artifactPath
		return result, 0, nil
	}
	reduction, err := backend.MinimizeSQLArtifact(artifact, maxTrials)
	if err != nil {
		return result, 2, err
	}
	reportRevisionDrift(stderr, artifact.Revision, reduction.Artifact.Revision)
	if err = persistArtifact(outputPath, reduction.Artifact); err != nil {
		return result, 2, err
	}
	result.Status, result.Failure = "minimized", &reduction.Artifact.Failure
	result.Artifact, result.Trials, result.ReductionStatus = outputPath, &reduction.Trials, reduction.Status
	result.ObservedRows = reduction.Artifact.ObservedRows
	return result, 0, nil
}

func decodeFile[T any](path string, decode func(io.Reader) (T, error)) (value T, err error) {
	file, err := os.Open(path)
	if err != nil {
		return value, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	value, err = decode(file)
	if err != nil {
		err = fmt.Errorf("decode %s: %w", path, err)
	}
	return value, err
}

func reportRevisionDrift(stderr io.Writer, recorded, current string) {
	if recorded != current {
		fmt.Fprintf(stderr, "replay: producer revision drift: artifact=%s current=%s; semantic replay verified\n", recorded, current)
	}
}

func persistArtifact(destination string, artifact any) (err error) {
	file, err := os.CreateTemp(filepath.Dir(destination), ".replay-*.tmp")
	if err != nil {
		return fmt.Errorf("create artifact temporary file: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(file.Name()); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove artifact temporary file: %w", removeErr))
		}
	}()
	if err := json.NewEncoder(file).Encode(artifact); err != nil {
		return errors.Join(fmt.Errorf("encode artifact: %w", err), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync artifact: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close artifact: %w", err)
	}
	if err := os.Link(file.Name(), destination); err != nil {
		return fmt.Errorf("publish artifact without overwriting %s: %w", destination, err)
	}
	return nil
}
