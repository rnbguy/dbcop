package backend

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type sqlReadCandidate struct {
	val     interface{}
	id      Version
	initial bool
}

type sqlReplayController struct {
	prefix                  []int
	prefixMode              bool
	strict                  bool
	reduction               bool
	expected                []ReplayChoice
	newest                  *ReplayChoice
	transaction             string
	statement               string
	ordinal                 int
	choices                 []ReplayChoice
	err                     error
	tables                  map[string][]string
	keys                    map[string]string
	sources                 map[Version]ReplaySource
	retained                map[sqlReadSite]ReplayChoice
	seen                    map[sqlReadSite]bool
	branch                  *sqlReplayBranch
	decisionLimit           *int
	candidateKeysForTesting func(*Table) []string
}

type sqlReadSite struct {
	transaction string
	statement   string
	ordinal     int
	key         string
}

func sqlChoiceSite(choice ReplayChoice) sqlReadSite {
	return sqlReadSite{choice.TransactionID, choice.StatementID, choice.ReadOrdinal, choice.Key}
}

// Each node describes one admitted universe after an exact source-choice prefix.
type sqlReplayBranch struct {
	choice   *ReplayChoice
	children map[int]*sqlReplayBranch
	terminal bool
}

func (controller *sqlReplayController) initialize(store *Store) error {
	controller.choices = make([]ReplayChoice, 0)
	controller.tables = make(map[string][]string)
	controller.keys = make(map[string]string)
	controller.sources = make(map[Version]ReplaySource)
	type tableGeneration struct {
		namespace string
		number    uint64
		table     *Table
	}
	tables := make([]tableGeneration, 0, len(store.tables))
	for namespace, table := range store.tables {
		if !strings.HasPrefix(namespace, "\x00sql:") {
			return fmt.Errorf("trace namespace: non-SQL table %q", namespace)
		}
		rest := strings.TrimPrefix(namespace, "\x00sql:")
		end := strings.IndexByte(rest, ':')
		if end < 0 {
			return fmt.Errorf("trace namespace: invalid SQL table namespace")
		}
		number, err := strconv.ParseUint(rest[:end], 10, 64)
		if err != nil {
			return fmt.Errorf("trace namespace: %w", err)
		}
		tables = append(tables, tableGeneration{namespace, number, table})
	}
	slices.SortFunc(tables, func(a, b tableGeneration) int { return cmp.Compare(a.number, b.number) })
	for index, generation := range tables {
		controller.tables[generation.namespace] = []string{generation.table.databaseName, generation.table.def.Name, strconv.Itoa(index + 1)}
	}
	if controller.reduction {
		controller.retained = make(map[sqlReadSite]ReplayChoice, len(controller.expected))
		controller.seen = make(map[sqlReadSite]bool, len(controller.expected))
		for _, choice := range controller.expected {
			site := sqlChoiceSite(choice)
			if _, exists := controller.retained[site]; exists {
				return fmt.Errorf("reduction divergence: duplicate retained read site")
			}
			controller.retained[site] = choice
		}
		if controller.newest != nil {
			if _, exists := controller.retained[sqlChoiceSite(*controller.newest)]; !exists {
				return fmt.Errorf("reduction divergence: newest target is not retained")
			}
		}
	}
	return nil
}

func (controller *sqlReplayController) cell(tab, pkey, column string) (string, error) {
	raw := cellKey(tab, pkey, column)
	if key, exists := controller.keys[raw]; exists {
		return key, nil
	}
	table, exists := controller.tables[tab]
	if !exists {
		return "", fmt.Errorf("trace namespace: unregistered table %q", tab)
	}
	for _, part := range []string{table[0], table[1], pkey, column} {
		if !utf8.ValidString(part) {
			return "", fmt.Errorf("trace namespace: non-UTF-8 SQL identity")
		}
	}
	// A slice of validated strings has no unsupported JSON values or cycles.
	encoded, _ := json.Marshal([]string{table[0], table[1], table[2], pkey, column})
	key := string(encoded)
	controller.keys[raw] = key
	return key, nil
}

func (controller *sqlReplayController) fail(format string, args ...interface{}) (int, bool, bool) {
	if controller.err == nil {
		controller.err = fmt.Errorf(format, args...)
	}
	return 0, false, false
}

func (controller *sqlReplayController) selectRead(session *Session, tab, pkey, column string, candidates []sqlReadCandidate) (int, bool, bool) {
	if controller.err != nil {
		return 0, false, false
	}
	if controller.decisionLimit != nil && len(controller.choices) >= *controller.decisionLimit {
		return controller.fail("execution exceeds decision bound %d", *controller.decisionLimit)
	}
	key, err := controller.cell(tab, pkey, column)
	if err != nil {
		controller.err = err
		return 0, false, false
	}
	controller.ordinal++
	choice := ReplayChoice{TransactionID: controller.transaction, StatementID: controller.statement, ReadOrdinal: controller.ordinal, Reader: session.sid, Key: key, Candidates: make([]ReplaySource, len(candidates))}
	for index, candidate := range candidates {
		if candidate.initial {
			choice.Candidates[index] = ReplaySource{Initial: true, Key: key}
		} else {
			source, exists := controller.sources[candidate.id]
			if !exists || source.Key != key {
				return controller.fail("trace provenance: source for runtime version %d at %s missing", candidate.id, key)
			}
			choice.Candidates[index] = source
		}
	}
	if branch := controller.branch; branch != nil {
		if branch.terminal {
			return controller.fail("replay divergence: extra read after a completed source prefix")
		}
		if expected := branch.choice; expected != nil && !sameSQLUniverse(choice, *expected) {
			return controller.fail("replay divergence: read site or ordered admitted universe changed at decision %d", len(controller.choices))
		}
	}
	position := len(controller.choices)
	pick := len(candidates) - 1
	deviated := false
	switch {
	case controller.strict:
		if position >= len(controller.expected) {
			return controller.fail("replay divergence: extra read at %q/%q/%d", choice.TransactionID, choice.StatementID, choice.ReadOrdinal)
		}
		expected := controller.expected[position]
		if !sameSQLUniverse(choice, expected) {
			return controller.fail("replay divergence: read site or ordered candidates changed at decision %d", position)
		}
		// Artifact validation and equal candidate lists guarantee this selection.
		pick = expected.Choice
	case controller.reduction:
		site := sqlChoiceSite(choice)
		expected, exists := controller.retained[site]
		if !exists || controller.seen[site] || expected.Reader != choice.Reader {
			return controller.fail("reduction divergence: new or repeated read site at %q/%q/%d", choice.TransactionID, choice.StatementID, choice.ReadOrdinal)
		}
		controller.seen[site] = true
		if controller.newest == nil || sqlChoiceSite(*controller.newest) != site {
			pick = slices.Index(choice.Candidates, expected.Selected)
			if pick < 0 {
				return controller.fail("reduction divergence: retained source no longer admitted at %q/%q/%d", choice.TransactionID, choice.StatementID, choice.ReadOrdinal)
			}
		}
	case controller.prefixMode:
		if position < len(controller.prefix) {
			pick = controller.prefix[position]
			if pick < 0 || pick >= len(candidates) {
				return controller.fail("replay divergence: prefix index %d outside [0,%d) at decision %d", pick, len(candidates), position)
			}
		}
	default:
		pick, deviated = session.store.selectReadIndex(len(candidates), candidates[0].initial, session.liveTxn())
	}
	choice.Choice, choice.Selected, choice.RuntimeVersion = pick, choice.Candidates[pick], uint64(candidates[pick].id)
	if branch := controller.branch; branch != nil {
		if branch.choice == nil {
			snapshot := choice
			branch.choice = &snapshot
			branch.children = make(map[int]*sqlReplayBranch)
		}
		next := branch.children[pick]
		if next == nil {
			next = &sqlReplayBranch{}
			branch.children[pick] = next
		}
		controller.branch = next
	}
	controller.choices = append(controller.choices, choice)
	return pick, deviated, true
}

func sameSQLUniverse(actual, expected ReplayChoice) bool {
	return sqlChoiceSite(actual) == sqlChoiceSite(expected) && actual.Reader == expected.Reader && reflect.DeepEqual(actual.Candidates, expected.Candidates)
}

func (controller *sqlReplayController) finish() error {
	if controller.prefixMode && len(controller.prefix) > len(controller.choices) {
		return fmt.Errorf("replay divergence: unused prefix choices (%d of %d consumed)", len(controller.choices), len(controller.prefix))
	}
	if controller.strict && len(controller.expected) != len(controller.choices) {
		return fmt.Errorf("replay divergence: missing reads (%d of %d observed)", len(controller.choices), len(controller.expected))
	}
	if controller.reduction && len(controller.seen) != len(controller.retained) {
		return fmt.Errorf("reduction divergence: missing retained read sites")
	}
	if branch := controller.branch; branch != nil {
		if branch.choice != nil {
			return fmt.Errorf("replay divergence: missing reads after a known source prefix")
		}
		branch.terminal = true
	}
	return nil
}

func (controller *sqlReplayController) history(log []LoggedTxn) (json.RawMessage, map[string]uint64, error) {
	keys := slices.AppendSeq(make([]string, 0, len(controller.keys)), maps.Values(controller.keys))
	slices.Sort(keys)
	variables := make(map[string]uint64, len(keys))
	for _, key := range keys {
		if _, exists := variables[key]; !exists {
			variables[key] = uint64(len(variables) + 1)
		}
	}
	versions := make(map[Version]uint64)
	for _, transaction := range log {
		for _, event := range transaction.Events {
			if event.Write != nil {
				versions[event.Write.ID] = uint64(len(versions) + 1)
			}
		}
	}
	sessions := make(map[int][]historyTxn)
	for _, transaction := range log {
		txn := historyTxn{Events: make([]historyEvent, 0, len(transaction.Events)), Committed: true}
		for _, event := range transaction.Events {
			if event.Write != nil {
				key, exists := controller.keys[event.Write.Key]
				if !exists {
					return nil, nil, fmt.Errorf("trace history: unknown write key")
				}
				version := versions[event.Write.ID]
				txn.Events = append(txn.Events, historyEvent{Write: &historyRW{Variable: variables[key], Version: &version}})
			} else if event.Read != nil {
				key, exists := controller.keys[event.Read.Key]
				if !exists {
					return nil, nil, fmt.Errorf("trace history: unknown read key")
				}
				read := historyRW{Variable: variables[key]}
				if !event.Read.Initial {
					version, exists := versions[event.Read.WriteID]
					if !exists {
						return nil, nil, fmt.Errorf("trace history: unknown read source")
					}
					read.Version = &version
				}
				txn.Events = append(txn.Events, historyEvent{Read: &read})
			} else {
				return nil, nil, fmt.Errorf("trace history: empty event")
			}
		}
		sessions[transaction.Session] = append(sessions[transaction.Session], txn)
	}
	ids := slices.Sorted(maps.Keys(sessions))
	history := make([][]historyTxn, 0, len(ids))
	for _, id := range ids {
		history = append(history, sessions[id])
	}
	encoded, err := json.Marshal(history)
	return encoded, variables, err
}
