package backend

import (
	"reflect"

	"github.com/dolthub/go-mysql-server/sql"
)

type keylessRow struct {
	key string
	row sql.Row
}

func (w *rowWriter) beginKeylessRows() {
	if !w.captureRows || len(w.table.primaryOrdinals()) != 0 {
		return
	}
	txn := w.statementTxn
	txn.mu.Lock()
	defer txn.mu.Unlock()
	if txn.keylessRows == nil {
		txn.keylessRows = make(map[string][]keylessRow)
	}
	if _, active := txn.keylessRows[w.table.storageName]; !active {
		txn.keylessRows[w.table.storageName] = nil
	}
}

func (t *Txn) recordKeylessRow(table, key string, row sql.Row) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, active := t.keylessRows[table]; active {
		t.keylessRows[table] = append(t.keylessRows[table], keylessRow{key: key, row: row})
	}
}

func (w *rowWriter) selectedKeylessRow(row sql.Row) (string, bool) {
	if w.statementTxn == nil {
		return "", false
	}
	txn := w.statementTxn
	txn.mu.Lock()
	defer txn.mu.Unlock()
	for _, selected := range txn.keylessRows[w.table.storageName] {
		if !w.used[selected.key] && reflect.DeepEqual(row, selected.row) {
			if w.used == nil {
				w.used = make(map[string]bool)
			}
			w.used[selected.key] = true
			return selected.key, true
		}
	}
	return "", false
}

func (w *rowWriter) endKeylessRows() {
	if !w.captureRows || w.statementTxn == nil {
		return
	}
	w.statementTxn.mu.Lock()
	delete(w.statementTxn.keylessRows, w.table.storageName)
	w.statementTxn.mu.Unlock()
}
