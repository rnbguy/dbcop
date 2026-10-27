// Package backend: databases and schema-driven versioned tables.
package backend

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/mysql"
)

// Physical table namespaces survive DROP/CREATE without aliasing old versions.
var nextTableNamespace atomic.Uint64

type Provider struct {
	mu          sync.RWMutex
	store       *Store
	iso         Isolation
	databases   map[string]*Database
	procedures  sql.ExternalStoredProcedureRegistry
	foreignKeys foreignKeyState
}

// NewProvider starts empty when defs is empty. Legacy definitions are installed
// in the legacy default database; SQL-created databases have no fixed name.
func NewProvider(store *Store, iso Isolation, defs []TableDef) *Provider {
	p := &Provider{store: store, iso: iso, databases: make(map[string]*Database)}
	p.procedures = sql.NewExternalStoredProcedureRegistry()
	p.procedures.Register(sql.ExternalStoredProcedureDetails{
		Name:     "dbcop_seal_initial_state",
		Function: p.sealInitialState,
	})
	p.procedures.Register(sql.ExternalStoredProcedureDetails{
		Name:     "dbcop_coverage",
		Schema:   sql.Schema{&sql.Column{Name: "coverage", Type: types.LongText}},
		Function: p.committedCoverage,
		ReadOnly: true,
	})
	if len(defs) == 0 {
		return p
	}
	db := p.newDatabase("tpcc")
	for _, def := range defs {
		db.tables[strings.ToLower(def.Name)] = db.newTable(def, sql.Collation_Default)
	}
	p.databases["tpcc"] = db
	return p
}

var _ sql.DatabaseProvider = (*Provider)(nil)
var _ sql.MutableDatabaseProvider = (*Provider)(nil)
var _ sql.ExternalStoredProcedureProvider = (*Provider)(nil)

func (p *Provider) newDatabase(name string) *Database {
	return &Database{name: name, store: p.store, iso: p.iso, tables: make(map[string]*Table), foreignKeys: &p.foreignKeys}
}

func (p *Provider) HasDatabase(_ *sql.Context, name string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.databases[strings.ToLower(name)] != nil
}

func (p *Provider) AllDatabases(_ *sql.Context) []sql.Database {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := slices.Sorted(maps.Keys(p.databases))
	databases := make([]sql.Database, len(names))
	for i, name := range names {
		databases[i] = p.databases[name]
	}
	return databases
}

func (p *Provider) Database(_ *sql.Context, name string) (sql.Database, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if db := p.databases[strings.ToLower(name)]; db != nil {
		return db, nil
	}
	return nil, mysql.NewSQLError(mysql.ERBadDb, mysql.SSClientError, "Unknown database '%s'", name)
}

func (p *Provider) CreateDatabase(_ *sql.Context, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToLower(name)
	if p.databases[key] != nil {
		return sql.ErrDatabaseExists.New(name)
	}
	p.databases[key] = p.newDatabase(name)
	return nil
}

func (p *Provider) DropDatabase(_ *sql.Context, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToLower(name)
	if p.databases[key] == nil {
		return sql.ErrDatabaseNotFound.New(name)
	}
	delete(p.databases, key)
	p.foreignKeys.dropDatabase(name)
	return nil
}

type Database struct {
	name        string
	store       *Store
	iso         Isolation
	mu          sync.RWMutex
	tables      map[string]*Table
	foreignKeys *foreignKeyState
}

var _ sql.Database = (*Database)(nil)
var _ sql.TableCreator = (*Database)(nil)
var _ sql.TableDropper = (*Database)(nil)

func (d *Database) Name() string { return d.name }

func (d *Database) newTable(def TableDef, collation sql.CollationID) *Table {
	namespace := "\x00sql:" + strconv.FormatUint(nextTableNamespace.Add(1), 10) + ":" + d.name + "." + def.Name
	table := newTable(def, d.store, d.iso, d.name, namespace, d.foreignKeys)
	table.collation = collation
	return table
}

func (d *Database) GetTableInsensitive(_ *sql.Context, name string) (sql.Table, bool, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	table, ok := d.tables[strings.ToLower(name)]
	return table, ok, nil
}

func (d *Database) GetTableNames(_ *sql.Context) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	names := make([]string, 0, len(d.tables))
	for _, table := range d.tables {
		names = append(names, table.Name())
	}
	slices.Sort(names)
	return names, nil
}

// No PK means physical row IDs, not an implicit key on the first user column.
func (d *Database) CreateTable(_ *sql.Context, name string, schema sql.PrimaryKeySchema, collation sql.CollationID, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := strings.ToLower(name)
	if d.tables[key] != nil {
		return sql.ErrTableAlreadyExists.New(name)
	}
	cols := make([]*sql.Column, len(schema.Schema))
	for i, column := range schema.Schema {
		if strings.ContainsRune(column.Name, 0) {
			return fmt.Errorf("column names cannot contain NUL")
		}
		copy := *column
		copy.Source = name
		cols[i] = &copy
	}
	ords := append([]int(nil), schema.PkOrdinals...)
	d.tables[key] = d.newTable(TableDef{Name: name, Columns: cols, PkeyIdx: -1, PkeyIdxs: ords}, collation)
	return nil
}

func (d *Database) DropTable(_ *sql.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := strings.ToLower(name)
	if d.tables[key] == nil {
		return sql.ErrTableNotFound.New(name)
	}
	d.foreignKeys.dropTable(d.name, name)
	delete(d.tables, key)
	return nil
}
