package backend

import (
	"context"
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// MySQL DDL from pingcap/go-tpc v1.0.12 tpcc/ddl.go, without optional
// partitioning or foreign keys (the standard client's defaults).
func TestStandardClientDDLOnEmptyProvider(t *testing.T) {
	f := newSQLTableFixture(t)
	if databases := f.provider.AllDatabases(f.ctx); len(databases) != 0 {
		t.Fatalf("empty provider predeclared databases: %v", databases)
	}
	f.query(t, "CREATE DATABASE client_owned")
	f.query(t, "USE client_owned")
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS warehouse (
 w_id INT NOT NULL, w_name VARCHAR(10), w_street_1 VARCHAR(20), w_street_2 VARCHAR(20),
 w_city VARCHAR(20), w_state CHAR(2), w_zip CHAR(9), w_tax DECIMAL(4,4), w_ytd DECIMAL(12,2),
 PRIMARY KEY (w_id) /*T![clustered_index] CLUSTERED */)`,
		`CREATE TABLE IF NOT EXISTS district (
 d_id INT NOT NULL, d_w_id INT NOT NULL, d_name VARCHAR(10), d_street_1 VARCHAR(20),
 d_street_2 VARCHAR(20), d_city VARCHAR(20), d_state CHAR(2), d_zip CHAR(9), d_tax DECIMAL(4,4),
 d_ytd DECIMAL(12,2), d_next_o_id INT, PRIMARY KEY (d_w_id,d_id) /*T![clustered_index] CLUSTERED */)`,
		`CREATE TABLE IF NOT EXISTS customer (
 c_id INT NOT NULL, c_d_id INT NOT NULL, c_w_id INT NOT NULL, c_first VARCHAR(16), c_middle CHAR(2),
 c_last VARCHAR(16), c_street_1 VARCHAR(20), c_street_2 VARCHAR(20), c_city VARCHAR(20), c_state CHAR(2),
 c_zip CHAR(9), c_phone CHAR(16), c_since DATETIME, c_credit CHAR(2), c_credit_lim DECIMAL(12,2),
 c_discount DECIMAL(4,4), c_balance DECIMAL(12,2), c_ytd_payment DECIMAL(12,2), c_payment_cnt INT,
 c_delivery_cnt INT, c_data VARCHAR(500), PRIMARY KEY(c_w_id,c_d_id,c_id) /*T![clustered_index] CLUSTERED */,
 INDEX idx_customer (c_w_id,c_d_id,c_last,c_first))`,
		`CREATE TABLE IF NOT EXISTS history (
 h_c_id INT NOT NULL, h_c_d_id INT NOT NULL, h_c_w_id INT NOT NULL, h_d_id INT NOT NULL,
 h_w_id INT NOT NULL, h_date DATETIME, h_amount DECIMAL(6,2), h_data VARCHAR(24),
 INDEX idx_h_w_id (h_w_id), INDEX idx_h_c_w_id (h_c_w_id))`,
		`CREATE TABLE IF NOT EXISTS new_order (
 no_o_id INT NOT NULL, no_d_id INT NOT NULL, no_w_id INT NOT NULL,
 PRIMARY KEY(no_w_id,no_d_id,no_o_id) /*T![clustered_index] CLUSTERED */)`,
		`CREATE TABLE IF NOT EXISTS orders (
 o_id INT NOT NULL, o_d_id INT NOT NULL, o_w_id INT NOT NULL, o_c_id INT, o_entry_d DATETIME,
 o_carrier_id INT, o_ol_cnt INT, o_all_local INT,
 PRIMARY KEY(o_w_id,o_d_id,o_id) /*T![clustered_index] CLUSTERED */,
 INDEX idx_order (o_w_id,o_d_id,o_c_id,o_id))`,
		`CREATE TABLE IF NOT EXISTS order_line (
 ol_o_id INT NOT NULL, ol_d_id INT NOT NULL, ol_w_id INT NOT NULL, ol_number INT NOT NULL,
 ol_i_id INT NOT NULL, ol_supply_w_id INT, ol_delivery_d DATETIME, ol_quantity INT,
 ol_amount DECIMAL(6,2), ol_dist_info CHAR(24),
 PRIMARY KEY(ol_w_id,ol_d_id,ol_o_id,ol_number) /*T![clustered_index] CLUSTERED */)`,
		`CREATE TABLE IF NOT EXISTS stock (
 s_i_id INT NOT NULL, s_w_id INT NOT NULL, s_quantity INT, s_dist_01 CHAR(24), s_dist_02 CHAR(24),
 s_dist_03 CHAR(24), s_dist_04 CHAR(24), s_dist_05 CHAR(24), s_dist_06 CHAR(24), s_dist_07 CHAR(24),
 s_dist_08 CHAR(24), s_dist_09 CHAR(24), s_dist_10 CHAR(24), s_ytd INT, s_order_cnt INT,
 s_remote_cnt INT, s_data VARCHAR(50), PRIMARY KEY(s_w_id,s_i_id) /*T![clustered_index] CLUSTERED */)`,
		`CREATE TABLE IF NOT EXISTS item (
 i_id INT NOT NULL, i_im_id INT, i_name VARCHAR(24), i_price DECIMAL(5,2), i_data VARCHAR(50),
 PRIMARY KEY(i_id) /*T![clustered_index] CLUSTERED */)`,
	} {
		f.query(t, ddl)
	}
	db, err := f.provider.Database(f.ctx, "CLIENT_OWNED")
	if err != nil {
		t.Fatal(err)
	}
	names, err := db.GetTableNames(f.ctx)
	if err != nil || len(names) != 9 {
		t.Fatalf("standard client schema incomplete: %v, %v", names, err)
	}
	raw, _, _ := db.GetTableInsensitive(f.ctx, "history")
	if ords := raw.(sql.PrimaryKeyTable).PrimaryKeySchema().PkOrdinals; len(ords) != 0 {
		t.Fatalf("history acquired an implicit primary key: %v", ords)
	}
	indexes, err := raw.(*Table).GetIndexes(f.ctx)
	if err != nil || len(indexes) != 2 {
		t.Fatalf("history index metadata lost: %v, %v", indexes, err)
	}
	raw, _, _ = db.GetTableInsensitive(f.ctx, "district")
	if ords := raw.(sql.PrimaryKeyTable).PrimaryKeySchema().PkOrdinals; !reflect.DeepEqual(ords, []int{1, 0}) {
		t.Fatalf("engine composite-key order lost: %v", ords)
	}
}

func TestDatabaseAndTableRecreationUseIndependentStorage(t *testing.T) {
	provider := NewProvider(NewStore(1, 2), ReadCommitted, nil)
	RegisterVariables()
	session := NewSession(provider.store, ReadCommitted, 1)
	ctx := sql.NewContext(context.Background(), sql.WithSession(session))
	session.beginTxn()
	t.Cleanup(func() { _ = session.Rollback(ctx, nil) })
	for _, name := range []string{"first", "second"} {
		if err := provider.CreateDatabase(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	schema := sql.NewPrimaryKeySchema(sql.Schema{{Name: "id", Type: types.Int64, PrimaryKey: true}}, 0)
	create := func(database string) *Table {
		t.Helper()
		db, err := provider.Database(ctx, database)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.(*Database).CreateTable(ctx, "same", schema, sql.Collation_Default, ""); err != nil {
			t.Fatal(err)
		}
		raw, _, _ := db.GetTableInsensitive(ctx, "SAME")
		return raw.(*Table)
	}
	first, second := create("first"), create("second")
	for i, table := range []*Table{first, second} {
		if err := table.Inserter(ctx).Insert(ctx, sql.Row{int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	if rows := tableRows(t, ctx, first); !reflect.DeepEqual(rows, []sql.Row{{int64(1)}}) {
		t.Fatalf("first database contains another database's rows: %v", rows)
	}
	if rows := tableRows(t, ctx, second); !reflect.DeepEqual(rows, []sql.Row{{int64(2)}}) {
		t.Fatalf("second database contains another database's rows: %v", rows)
	}
	db, _ := provider.Database(ctx, "first")
	if err := db.(*Database).DropTable(ctx, "same"); err != nil {
		t.Fatal(err)
	}
	if rows := tableRows(t, ctx, create("first")); len(rows) != 0 {
		t.Fatalf("recreated table observed dropped table rows: %v", rows)
	}
	if err := provider.DropDatabase(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if provider.HasDatabase(ctx, "FIRST") {
		t.Fatal("dropped database remains visible")
	}
	if err := provider.CreateDatabase(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if rows := tableRows(t, ctx, create("first")); len(rows) != 0 {
		t.Fatalf("recreated database observed dropped database rows: %v", rows)
	}
}

func TestSQLSchemaRejectsMembershipNamespaceAsColumn(t *testing.T) {
	provider := NewProvider(NewStore(1, 2), ReadCommitted, nil)
	ctx := sql.NewEmptyContext()
	if err := provider.CreateDatabase(ctx, "metadata"); err != nil {
		t.Fatal(err)
	}
	db, _ := provider.Database(ctx, "metadata")
	schema := sql.NewPrimaryKeySchema(sql.Schema{{Name: rowExistenceColumn, Type: types.Int64}})
	if err := db.(*Database).CreateTable(ctx, "invalid", schema, sql.Collation_Default, ""); err == nil {
		t.Fatal("SQL column could alias internal membership metadata")
	}
	if _, found, err := db.GetTableInsensitive(ctx, "invalid"); err != nil || found {
		t.Fatalf("invalid schema was registered: found=%v, err=%v", found, err)
	}
}
