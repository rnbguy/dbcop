package backend

import (
	"sort"
	"strings"
	"sync"

	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
)

// Share metadata across databases so parent edits also discover cross-database references.
type foreignKeyState struct {
	mu          sync.RWMutex
	collections map[string]*memory.ForeignKeyCollection
}

var _ sql.ForeignKeyTable = (*Table)(nil)
var _ sql.ForeignKeyEditor = (*rowWriter)(nil)

func (t *Table) GetDeclaredForeignKeys(_ *sql.Context) ([]sql.ForeignKeyConstraint, error) {
	t.foreignKeys.mu.RLock()
	defer t.foreignKeys.mu.RUnlock()
	var keys []sql.ForeignKeyConstraint
	for _, fk := range t.foreignKeys.collections[strings.ToLower(t.databaseName)].Keys() {
		if strings.EqualFold(fk.Table, t.Name()) {
			keys = append(keys, fk)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return keys, nil
}

func (t *Table) GetReferencedForeignKeys(_ *sql.Context) ([]sql.ForeignKeyConstraint, error) {
	t.foreignKeys.mu.RLock()
	defer t.foreignKeys.mu.RUnlock()
	var keys []sql.ForeignKeyConstraint
	for _, collection := range t.foreignKeys.collections {
		for _, fk := range collection.Keys() {
			if strings.EqualFold(fk.ParentDatabase, t.databaseName) && strings.EqualFold(fk.ParentTable, t.Name()) {
				keys = append(keys, fk)
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Database != keys[j].Database {
			return keys[i].Database < keys[j].Database
		}
		return keys[i].Name < keys[j].Name
	})
	return keys, nil
}

func (t *Table) AddForeignKey(_ *sql.Context, fk sql.ForeignKeyConstraint) error {
	t.foreignKeys.mu.Lock()
	defer t.foreignKeys.mu.Unlock()
	if t.foreignKeys.collections == nil {
		t.foreignKeys.collections = make(map[string]*memory.ForeignKeyCollection)
	}
	key := strings.ToLower(t.databaseName)
	collection := t.foreignKeys.collections[key]
	if collection == nil {
		collection = &memory.ForeignKeyCollection{}
		t.foreignKeys.collections[key] = collection
	}
	for _, existing := range collection.Keys() {
		if strings.EqualFold(existing.Name, fk.Name) {
			return sql.ErrForeignKeyDuplicateName.New(fk.Name)
		}
	}
	collection.AddFK(fk)
	return nil
}

func (t *Table) DropForeignKey(_ *sql.Context, name string) error {
	t.foreignKeys.mu.Lock()
	defer t.foreignKeys.mu.Unlock()
	collection := t.foreignKeys.collections[strings.ToLower(t.databaseName)]
	for _, fk := range collection.Keys() {
		if strings.EqualFold(fk.Table, t.Name()) && strings.EqualFold(fk.Name, name) {
			collection.DropFK(name)
			return nil
		}
	}
	return sql.ErrForeignKeyNotFound.New(name, t.Name())
}

func (t *Table) UpdateForeignKey(_ *sql.Context, name string, fk sql.ForeignKeyConstraint) error {
	t.foreignKeys.mu.Lock()
	defer t.foreignKeys.mu.Unlock()
	collection := t.foreignKeys.collections[strings.ToLower(t.databaseName)]
	position := -1
	for i, existing := range collection.Keys() {
		if strings.EqualFold(existing.Name, name) && strings.EqualFold(existing.Table, t.Name()) {
			position = i
		} else if strings.EqualFold(existing.Name, fk.Name) {
			return sql.ErrForeignKeyDuplicateName.New(fk.Name)
		}
	}
	if position < 0 {
		return sql.ErrForeignKeyNotFound.New(name, t.Name())
	}
	collection.Keys()[position] = fk
	return nil
}

func (t *Table) CreateIndexForForeignKey(ctx *sql.Context, def sql.IndexDef) error {
	return t.CreateIndex(ctx, def)
}

func (t *Table) GetForeignKeyEditor(ctx *sql.Context) sql.ForeignKeyEditor {
	copy := *t
	copy.proj, copy.filters, copy.pushed = nil, nil, nil
	return &rowWriter{table: &copy, sess: sessionOf(ctx), captureRows: true}
}

func (w *rowWriter) IndexedAccess(ctx *sql.Context, lookup sql.IndexLookup) sql.IndexedTable {
	// Staged KV writes and index postings are already visible to this session.
	return w.table.IndexedAccess(ctx, lookup)
}

func (w *rowWriter) GetIndexes(ctx *sql.Context) ([]sql.Index, error) {
	return w.table.GetIndexes(ctx)
}

func (w *rowWriter) PreciseMatch() bool { return w.table.PreciseMatch() }

func (s *foreignKeyState) dropTable(database, table string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	collection := s.collections[strings.ToLower(database)]
	keys := collection.Keys()
	for i := len(keys) - 1; i >= 0; i-- {
		if strings.EqualFold(keys[i].Table, table) {
			collection.DropFK(keys[i].Name)
		}
	}
	s.unresolveParent(database, table)
}

func (s *foreignKeyState) dropDatabase(database string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.collections, strings.ToLower(database))
	s.unresolveParent(database, "")
}

// Called with the metadata lock held; recreated parents must be resolved again.
func (s *foreignKeyState) unresolveParent(database, table string) {
	for _, collection := range s.collections {
		for i, fk := range collection.Keys() {
			if strings.EqualFold(fk.ParentDatabase, database) && (table == "" || strings.EqualFold(fk.ParentTable, table)) {
				collection.Keys()[i].IsResolved = false
			}
		}
	}
}
