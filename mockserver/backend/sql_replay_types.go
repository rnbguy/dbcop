package backend

import (
	"encoding/json"
	"time"

	"github.com/rnbguy/dbcop/mockserver/exploration"
)

// SQLWorkload fixes SQL input and transaction order; read choices remain weak.
type SQLWorkload struct {
	SchemaVersion        int              `json:"schema_version"`
	Isolation            string           `json:"isolation"`
	Setup                []string         `json:"setup"`
	Transactions         []SQLTransaction `json:"transactions"`
	Assertion            SQLAssertion     `json:"assertion"`
	Seed                 *uint64          `json:"seed,omitempty"`
	ReadPolicy           string           `json:"read_policy,omitempty"`
	DeviationProbability *float64         `json:"deviation_probability,omitempty"`
	DeviationBudget      *int             `json:"deviation_budget,omitempty"`
}

// SQLTransaction is executed as a whole before the next transaction starts.
type SQLTransaction struct {
	ID         string         `json:"id"`
	SessionID  int            `json:"session_id"`
	Statements []SQLStatement `json:"statements"`
	Finish     string         `json:"finish"`
}

// SQLStatement IDs survive deletion of other statements during reduction.
type SQLStatement struct {
	ID  string `json:"id"`
	SQL string `json:"sql"`
}

type SQLAssertion struct {
	ID           string     `json:"id"`
	SQL          string     `json:"sql"`
	BaselineRows []TypedRow `json:"baseline_rows"`
	WantRows     []TypedRow `json:"want_rows"`
}

// TypedValue preserves SQL NULL, exact numbers, text, and binary distinctions.
type TypedValue struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type TypedRow []TypedValue

// ReplaySource identifies a write without process-local table/version IDs.
type ReplaySource struct {
	Initial       bool   `json:"initial"`
	TransactionID string `json:"transaction_id,omitempty"`
	StatementID   string `json:"statement_id,omitempty"`
	WriteOrdinal  int    `json:"write_ordinal,omitempty"`
	Key           string `json:"key"`
}

// ReplayChoice records the admitted universe and source at one external read.
type ReplayChoice struct {
	TransactionID  string         `json:"transaction_id"`
	StatementID    string         `json:"statement_id"`
	ReadOrdinal    int            `json:"read_ordinal"`
	Reader         int            `json:"reader"`
	Key            string         `json:"key"`
	Choice         int            `json:"choice"`
	Candidates     []ReplaySource `json:"candidates"`
	Selected       ReplaySource   `json:"selected"`
	RuntimeVersion uint64         `json:"runtime_version"`
}

type FailureFingerprint struct {
	AssertionID string `json:"assertion_id"`
	Kind        string `json:"kind"`
}

// SQLResult separates assertion mismatches from execution errors.
type SQLResult struct {
	Workload     SQLWorkload                   `json:"workload"`
	Revision     string                        `json:"producer_revision"`
	Choices      []ReplayChoice                `json:"choices"`
	ObservedRows []TypedRow                    `json:"observed_rows"`
	History      json.RawMessage               `json:"history"`
	Variables    map[string]uint64             `json:"variables"`
	Failure      *FailureFingerprint           `json:"failure,omitempty"`
	Transactions exploration.TransactionCounts `json:"transactions"`
	Elapsed      time.Duration                 `json:"workload_elapsed_ns"`
}

// ReplayArtifact contains the complete fixture and semantic execution evidence.
type ReplayArtifact struct {
	SchemaVersion    int                `json:"schema_version"`
	Revision         string             `json:"producer_revision"`
	ValidationPolicy string             `json:"validation_policy"`
	Workload         SQLWorkload        `json:"workload"`
	Choices          []ReplayChoice     `json:"choices"`
	ObservedRows     []TypedRow         `json:"observed_rows"`
	History          json.RawMessage    `json:"history"`
	Variables        map[string]uint64  `json:"variables"`
	Failure          FailureFingerprint `json:"failure"`
}

type ReductionResult struct {
	Artifact ReplayArtifact `json:"artifact"`
	Trials   int            `json:"trials"`
	Status   string         `json:"status"`
}
