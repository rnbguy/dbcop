package exploration

// HistoryFeature is a read-from motif within one committed reader transaction.
// Session roles follow first appearance; keys and version IDs are not features.
// This bounded abstraction is coverage, not a history equivalence reduction.
type HistoryFeature struct {
	Reader          int  `json:"reader"`
	Writer          int  `json:"writer"`
	PreviousWriter  int  `json:"previous_writer"`
	HasPreviousRead bool `json:"has_previous_read"`
	Initial         bool `json:"initial"`
	SameVariable    bool `json:"same_variable"`
	SameSource      bool `json:"same_source_transaction"`
	SessionOrdered  bool `json:"has_session_predecessor"`
}

// FeatureObservation separates discovery position from semantic feature identity.
type FeatureObservation struct {
	Feature          HistoryFeature `json:"feature"`
	FirstTransaction int            `json:"first_transaction"`
}

// HistoryCoverage excludes aborted/live prefixes and application values.
// Backend committed transactions are not BenchBase's client-completion count.
type HistoryCoverage struct {
	Transactions int                  `json:"committed_transactions"`
	Reads        int                  `json:"external_reads"`
	Features     []FeatureObservation `json:"features"`
}
