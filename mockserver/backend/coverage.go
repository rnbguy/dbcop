package backend

import (
	"encoding/json"
	"fmt"

	"github.com/dolthub/go-mysql-server/sql"

	"github.com/rnbguy/dbcop/mockserver/exploration"
)

// CommittedCoverage records WR and same-reader WR/WR and SO/WR motifs.
func CommittedCoverage(history []LoggedTxn) (exploration.HistoryCoverage, error) {
	type source struct {
		key     string
		txn     int
		session int
	}
	coverage := exploration.HistoryCoverage{Transactions: len(history), Features: []exploration.FeatureObservation{}}
	roles := make(map[int]int)
	role := func(session int) int {
		if value, ok := roles[session]; ok {
			return value
		}
		value := len(roles) + 1
		roles[session] = value
		return value
	}
	writes := make(map[Version]source)
	seenSessions := make(map[int]bool)
	features := make(map[exploration.HistoryFeature]bool)
	for index, txn := range history {
		reader := role(txn.Session)
		var previous LoggedRead
		previousWriter, previousSource, hasPrevious := 0, -1, false
		for _, read := range txn.Reads {
			writer, sourceTxn := 0, -1
			if !read.Initial {
				from, ok := writes[read.WriteID]
				if !ok || from.key != read.Key {
					return exploration.HistoryCoverage{}, fmt.Errorf("coverage: transaction %d read has no preceding source for its key", index)
				}
				writer, sourceTxn = role(from.session), from.txn
			}
			feature := exploration.HistoryFeature{Reader: reader, Writer: writer, PreviousWriter: previousWriter,
				HasPreviousRead: hasPrevious, Initial: read.Initial,
				SameVariable:   hasPrevious && previous.Key == read.Key,
				SameSource:     hasPrevious && previousSource == sourceTxn,
				SessionOrdered: seenSessions[txn.Session]}
			if !features[feature] {
				features[feature] = true
				coverage.Features = append(coverage.Features, exploration.FeatureObservation{Feature: feature, FirstTransaction: index + 1})
			}
			coverage.Reads++
			previous, previousWriter, previousSource, hasPrevious = read, writer, sourceTxn, true
		}
		for _, write := range txn.Writes {
			if _, exists := writes[write.ID]; exists {
				return exploration.HistoryCoverage{}, fmt.Errorf("coverage: duplicate committed write version")
			}
			writes[write.ID] = source{key: write.Key, txn: index, session: txn.Session}
		}
		seenSessions[txn.Session] = true
	}
	return coverage, nil
}

func (p *Provider) committedCoverage(_ *sql.Context) (sql.RowIter, error) {
	coverage, err := CommittedCoverage(p.store.CommittedHistory())
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(coverage)
	if err != nil {
		return nil, err
	}
	return sql.RowsToRowIter(sql.Row{string(data)}), nil
}
