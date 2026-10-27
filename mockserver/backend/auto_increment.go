package backend

import (
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

var _ sql.AutoIncrementTable = (*Table)(nil)
var _ sql.AutoIncrementSetter = (*rowWriter)(nil)

func (t *Table) PeekNextAutoIncrementValue(_ *sql.Context) (uint64, error) {
	t.state.mu.RLock()
	defer t.state.mu.RUnlock()
	return t.state.autoIncrement, nil
}

func (t *Table) GetNextAutoIncrementValue(ctx *sql.Context, insertVal interface{}) (uint64, error) {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	// Zero marks exhaustion, not an ID that can be allocated after wrapping.
	if t.state.autoIncrement == 0 {
		return 0, sql.ErrValueOutOfRange.New("AUTO_INCREMENT", types.Uint64)
	}
	cmp, err := types.Uint64.Compare(ctx, insertVal, t.state.autoIncrement)
	if err != nil {
		return 0, err
	}
	if cmp > 0 && insertVal != nil {
		value, _, err := types.Uint64.Convert(ctx, insertVal)
		if err != nil {
			return 0, err
		}
		t.state.autoIncrement = value.(uint64)
	}
	next := t.state.autoIncrement
	if insertVal == nil || cmp >= 0 {
		// Reserve outside transaction state: rollback must not reuse generated IDs.
		t.state.autoIncrement++
	}
	return next, nil
}

// Native writes may receive explicit rows without an AutoIncrement expression.
func (t *Table) observeAutoIncrement(ctx *sql.Context, row sql.Row) error {
	for i, column := range t.def.Columns {
		if !column.AutoIncrement {
			continue
		}
		cmp, err := column.Type.Compare(ctx, row[i], column.Type.Zero())
		if err != nil || cmp <= 0 {
			return err
		}
		value, _, err := types.Uint64.Convert(ctx, row[i])
		if err != nil {
			return err
		}
		t.state.mu.Lock()
		if t.state.autoIncrement != 0 && value.(uint64) >= t.state.autoIncrement {
			t.state.autoIncrement = value.(uint64) + 1
		}
		t.state.mu.Unlock()
		return nil
	}
	return nil
}

func (t *Table) AutoIncrementSetter(ctx *sql.Context) sql.AutoIncrementSetter {
	return &rowWriter{table: t, sess: sessionOf(ctx)}
}

func (w *rowWriter) SetAutoIncrementValue(_ *sql.Context, value uint64) error {
	if value == 0 {
		value = 1
	}
	w.table.state.mu.Lock()
	w.table.state.autoIncrement = value
	w.table.state.mu.Unlock()
	return nil
}

func (w *rowWriter) AcquireAutoIncrementLock(_ *sql.Context) (func(), error) {
	w.table.state.autoIncrementMu.Lock()
	return w.table.state.autoIncrementMu.Unlock, nil
}
