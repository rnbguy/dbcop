//go:build cgo

package main

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dolthub/go-mysql-server/server"
	gmsql "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/rnbguy/dbcop/mockserver/backend"
)

func assertionSQLFixture(t *testing.T, statements ...string) (*sql.DB, string) {
	t.Helper()
	return assertionSQLFixtureWithHandler(t, func(h mysql.Handler) (mysql.Handler, error) { return h, nil }, statements...)
}

func assertionSQLFixtureWithHandler(t *testing.T, wrapper server.HandlerWrapper, statements ...string) (*sql.DB, string) {
	t.Helper()
	backend.RegisterVariables()
	store := backend.NewStore(1, 2)
	t.Cleanup(store.Close)
	if err := store.BeginInitialization(); err != nil {
		t.Fatal(err)
	}
	engine := backend.NewEngine(backend.NewProvider(store, backend.Serializable, nil))
	t.Cleanup(func() { _ = engine.Close() })
	srv, err := server.NewServerWithHandler(server.Config{Protocol: "tcp", Address: "127.0.0.1:0"}, engine,
		gmsql.NewContext, backend.SessionBuilder(store, backend.Serializable), nil, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- srv.Start() }()
	t.Cleanup(func() {
		_ = srv.Close()
		if err := <-finished; err != nil {
			t.Errorf("SQL server shutdown: %v", err)
		}
		srv.SessionManager().WaitForClosedConnections()
	})
	dsn := "root@tcp(" + srv.Listener.Addr().String() + ")/checks?timeout=2s&readTimeout=5s&writeTimeout=5s"
	db, err := sql.Open("mysql", strings.Replace(dsn, "/checks?", "/?", 1))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	assertionSQLExec(t, db, "CREATE DATABASE checks", "USE checks")
	assertionSQLExec(t, db, statements...)
	return db, dsn
}

func assertionSQLExec(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func assertionSQLResult(t *testing.T, dsn, benchmark string, want []Assertion) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := check(ctx, dsn, benchmark)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("check(%s) = %+v, %v; want %+v", benchmark, got, err, want)
	}
}

func TestCheckApplicationsSQLInvariantTransitions(t *testing.T) {
	t.Run("smallbank exact conservation", func(t *testing.T) {
		db, dsn := assertionSQLFixture(t,
			"CREATE TABLE accounts (custid BIGINT)", "CREATE TABLE savings (custid BIGINT, bal VARCHAR(80))",
			"CREATE TABLE checking (custid BIGINT, bal VARCHAR(80))", "INSERT INTO accounts VALUES (1),(2)",
			"INSERT INTO savings VALUES (1,'2500'),(2,'2500')", "INSERT INTO checking VALUES (1,'2500'),(2,'2500')")
		assertionSQLResult(t, dsn, "smallbank", []Assertion{{ID: "A13", Holds: true}})
		assertionSQLExec(t, db, "UPDATE checking SET bal='2499.9' WHERE custid=1", "UPDATE checking SET bal='2500.1' WHERE custid=2")
		assertionSQLResult(t, dsn, "smallbank", []Assertion{{ID: "A13", Holds: true}})
		assertionSQLExec(t, db, "UPDATE checking SET bal='2500.100000000000000001' WHERE custid=2")
		assertionSQLResult(t, dsn, "smallbank", []Assertion{{ID: "A13", Holds: false}})
	})
	t.Run("voter maintained cap", func(t *testing.T) {
		db, dsn := assertionSQLFixture(t, "CREATE TABLE CONTESTANTS (contestant_number BIGINT)",
			"CREATE TABLE VOTES (phone_number BIGINT)", "INSERT INTO CONTESTANTS VALUES (1)")
		values := make([]string, 1000)
		for i := range values {
			values[i] = "(2125551234)"
		}
		assertionSQLExec(t, db, "INSERT INTO VOTES VALUES "+strings.Join(values, ","))
		assertionSQLResult(t, dsn, "voter", []Assertion{{ID: "A14", Holds: true}})
		assertionSQLExec(t, db, "INSERT INTO VOTES VALUES (2125551234)")
		assertionSQLResult(t, dsn, "voter", []Assertion{{ID: "A14", Holds: false}})
	})
	t.Run("wikipedia projections diverge independently", func(t *testing.T) {
		db, dsn := assertionSQLFixture(t, "CREATE TABLE useracct (user_id BIGINT, user_editcount BIGINT)",
			"CREATE TABLE recentchanges (rc_user BIGINT)", "CREATE TABLE logging (log_user BIGINT)",
			"INSERT INTO useracct VALUES (1,2),(2,0)", "INSERT INTO recentchanges VALUES (1),(1)",
			"INSERT INTO logging VALUES (1),(1)")
		assertionSQLResult(t, dsn, "wikipedia", []Assertion{{"A15", true}, {"A16", true}, {"A17", true}})
		assertionSQLExec(t, db, "INSERT INTO recentchanges VALUES (1)")
		assertionSQLResult(t, dsn, "wikipedia", []Assertion{{"A15", false}, {"A16", true}, {"A17", false}})
		assertionSQLExec(t, db, "UPDATE useracct SET user_editcount=3 WHERE user_id=1", "INSERT INTO logging VALUES (1)")
		assertionSQLResult(t, dsn, "wikipedia", []Assertion{{"A15", true}, {"A16", true}, {"A17", true}})
	})
}

func assertionTPCCFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, dsn := assertionSQLFixture(t,
		"CREATE TABLE WAREHOUSE (W_ID BIGINT, W_YTD VARCHAR(80))",
		"CREATE TABLE DISTRICT (D_W_ID BIGINT, D_ID BIGINT, D_YTD VARCHAR(80), D_NEXT_O_ID BIGINT)",
		"CREATE TABLE OORDER (O_W_ID BIGINT, O_D_ID BIGINT, O_ID BIGINT, O_C_ID BIGINT, O_OL_CNT BIGINT, O_CARRIER_ID BIGINT)",
		"CREATE TABLE NEW_ORDER (NO_W_ID BIGINT, NO_D_ID BIGINT, NO_O_ID BIGINT)",
		"CREATE TABLE CUSTOMER (C_W_ID BIGINT, C_D_ID BIGINT, C_ID BIGINT, C_BALANCE VARCHAR(80), C_YTD_PAYMENT VARCHAR(80))",
		"CREATE TABLE HISTORY (H_W_ID BIGINT, H_D_ID BIGINT, H_C_W_ID BIGINT, H_C_D_ID BIGINT, H_C_ID BIGINT, H_AMOUNT VARCHAR(80))",
		"CREATE TABLE ORDER_LINE (OL_W_ID BIGINT, OL_D_ID BIGINT, OL_O_ID BIGINT, OL_AMOUNT VARCHAR(80), OL_DELIVERY_D VARCHAR(80))")
	s := tpccTestState(t)
	for id, amount := range s.warehouses {
		assertionSQLExec(t, db, fmt.Sprintf("INSERT INTO WAREHOUSE VALUES (%d,'%s')", id, amount.FloatString(2)))
	}
	for key, district := range s.districts {
		assertionSQLExec(t, db, fmt.Sprintf("INSERT INTO DISTRICT VALUES (%d,%d,'%s',%d)", key.warehouse, key.district, district.ytd.FloatString(2), district.next))
	}
	for key, order := range s.orders {
		carrier, delivery := "NULL", "NULL"
		if order.carrier {
			carrier, delivery = "7", "'2026-01-01'"
		}
		assertionSQLExec(t, db, fmt.Sprintf("INSERT INTO OORDER VALUES (%d,%d,%d,%d,%d,%s)", key.district.warehouse, key.district.district, key.id, order.customer, order.lines, carrier))
		for i := uint64(0); i < order.lines; i++ {
			assertionSQLExec(t, db, fmt.Sprintf("INSERT INTO ORDER_LINE VALUES (%d,%d,%d,'0',%s)", key.district.warehouse, key.district.district, key.id, delivery))
		}
	}
	for key := range s.newOrders {
		assertionSQLExec(t, db, fmt.Sprintf("INSERT INTO NEW_ORDER VALUES (%d,%d,%d)", key.district.warehouse, key.district.district, key.id))
	}
	for key, customer := range s.customers {
		assertionSQLExec(t, db, fmt.Sprintf("INSERT INTO CUSTOMER VALUES (%d,%d,%d,'%s','%s')", key.district.warehouse, key.district.district, key.id, customer.balance.FloatString(2), customer.payment.FloatString(2)),
			fmt.Sprintf("INSERT INTO HISTORY VALUES (%d,%d,%d,%d,%d,'10')", key.district.warehouse, key.district.district, key.district.warehouse, key.district.district, key.id))
	}
	return db, dsn
}

func TestCheckTPCCSQLProjectionAndCorruption(t *testing.T) {
	db, dsn := assertionTPCCFixture(t)
	want := make([]Assertion, 12)
	for i := range want {
		want[i] = Assertion{ID: fmt.Sprintf("A%d", i+1), Holds: true}
	}
	assertionSQLResult(t, dsn, "tpcc", want)
	assertionSQLExec(t, db, "UPDATE ORDER_LINE SET OL_AMOUNT='0.1' WHERE OL_W_ID=1 AND OL_D_ID=1 AND OL_O_ID=1")
	want[9].Holds, want[11].Holds = false, false
	assertionSQLResult(t, dsn, "tpcc", want)
	assertionSQLExec(t, db, "UPDATE CUSTOMER SET C_BALANCE='-9.8' WHERE C_W_ID=1 AND C_D_ID=1 AND C_ID=1")
	want[9].Holds, want[11].Holds = true, true
	assertionSQLResult(t, dsn, "tpcc", want)
}

func TestCheckTPCCSQLRejectsMalformedProjections(t *testing.T) {
	for _, table := range []struct {
		name, column string
		duplicate    bool
	}{
		{"WAREHOUSE", "W_ID", true}, {"DISTRICT", "D_W_ID", true}, {"OORDER", "O_W_ID", true},
		{"NEW_ORDER", "NO_W_ID", true}, {"CUSTOMER", "C_W_ID", true},
		{"HISTORY", "H_W_ID", false}, {"ORDER_LINE", "OL_W_ID", false},
	} {
		t.Run(table.name, func(t *testing.T) {
			db, dsn := assertionTPCCFixture(t)
			if table.duplicate {
				assertionSQLExec(t, db, "INSERT INTO "+table.name+" SELECT * FROM "+table.name+" LIMIT 1")
				if result, err := check(context.Background(), dsn, "tpcc"); err == nil || result != nil || !strings.Contains(err.Error(), "duplicate") {
					t.Fatalf("duplicate %s result=%v err=%v", table.name, result, err)
				}
			}
			assertionSQLExec(t, db, "UPDATE "+table.name+" SET "+table.column+"=-1")
			if result, err := check(context.Background(), dsn, "tpcc"); err == nil || result != nil || !strings.Contains(err.Error(), "SELECT ") {
				t.Fatalf("invalid unsigned %s result=%v err=%v", table.name, result, err)
			}
			assertionSQLExec(t, db, "DROP TABLE "+table.name)
			if result, err := check(context.Background(), dsn, "tpcc"); err == nil || result != nil || !strings.Contains(err.Error(), table.name) {
				t.Fatalf("missing %s result=%v err=%v", table.name, result, err)
			}
		})
	}
}

func TestCheckApplicationsSQLRejectsBrokenFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, benchmark, failure string
		statements               []string
	}{
		{"accounts query", "smallbank", "accounts", nil},
		{"savings query", "smallbank", "savings", []string{"CREATE TABLE accounts (custid BIGINT)", "INSERT INTO accounts VALUES (1)"}},
		{"checking query", "smallbank", "checking", []string{"CREATE TABLE accounts (custid BIGINT)", "CREATE TABLE savings (custid BIGINT, bal TEXT)"}},
		{"duplicate account", "smallbank", "duplicate account", []string{"CREATE TABLE accounts (custid BIGINT)", "CREATE TABLE savings (custid BIGINT, bal TEXT)", "CREATE TABLE checking (custid BIGINT, bal TEXT)", "INSERT INTO accounts VALUES (1),(1)"}},
		{"negative customer", "smallbank", "SELECT custid", []string{"CREATE TABLE accounts (custid BIGINT)", "INSERT INTO accounts VALUES (-1)"}},
		{"duplicate balance", "smallbank", "duplicate customer", []string{"CREATE TABLE accounts (custid BIGINT)", "CREATE TABLE savings (custid BIGINT, bal TEXT)", "INSERT INTO savings VALUES (1,'2500'),(1,'2500')"}},
		{"negative balance customer", "smallbank", "SELECT custid, bal", []string{"CREATE TABLE accounts (custid BIGINT)", "CREATE TABLE savings (custid BIGINT, bal TEXT)", "INSERT INTO savings VALUES (-1,'2500')"}},
		{"invalid balance", "smallbank", "invalid balance", []string{"CREATE TABLE accounts (custid BIGINT)", "CREATE TABLE savings (custid BIGINT, bal TEXT)", "INSERT INTO savings VALUES (1,'NaN')"}},
		{"contestants query", "voter", "CONTESTANTS", nil},
		{"empty contestants", "voter", "empty contestant", []string{"CREATE TABLE CONTESTANTS (contestant_number BIGINT)"}},
		{"votes query", "voter", "VOTES", []string{"CREATE TABLE CONTESTANTS (contestant_number BIGINT)", "INSERT INTO CONTESTANTS VALUES (1)"}},
		{"users query", "wikipedia", "useracct", nil},
		{"recent query", "wikipedia", "recentchanges", []string{"CREATE TABLE useracct (user_id BIGINT, user_editcount BIGINT)"}},
		{"logging query", "wikipedia", "logging", []string{"CREATE TABLE useracct (user_id BIGINT, user_editcount BIGINT)", "CREATE TABLE recentchanges (rc_user BIGINT)"}},
		{"empty users", "wikipedia", "empty user", []string{"CREATE TABLE useracct (user_id BIGINT, user_editcount BIGINT)", "CREATE TABLE recentchanges (rc_user BIGINT)", "CREATE TABLE logging (log_user BIGINT)"}},
		{"duplicate users", "wikipedia", "duplicate user", []string{"CREATE TABLE useracct (user_id BIGINT, user_editcount BIGINT)", "INSERT INTO useracct VALUES (1,0),(1,0)"}},
		{"negative edit count", "wikipedia", "SELECT user_id", []string{"CREATE TABLE useracct (user_id BIGINT, user_editcount BIGINT)", "INSERT INTO useracct VALUES (1,-1)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, dsn := assertionSQLFixture(t, tc.statements...)
			if result, err := check(context.Background(), dsn, tc.benchmark); err == nil || result != nil || !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("result=%v error=%v, want %q failure", result, err, tc.failure)
			}
		})
	}
}

func TestCheckValidationPrecedenceAndConnectionFailure(t *testing.T) {
	for _, tc := range []struct{ dsn, benchmark, want string }{
		{"", "unknown", "unknown benchmark"}, {"malformed", "unknown", "unknown benchmark"},
		{"", "smallbank", "--dsn is required"}, {"malformed", "smallbank", "invalid DSN"},
	} {
		if got, err := check(context.Background(), tc.dsn, tc.benchmark); err == nil || got != nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("check(%q,%q) = %v,%v; want %s", tc.dsn, tc.benchmark, got, err, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := check(ctx, "root@tcp(127.0.0.1:1)/checks", "smallbank"); err != context.Canceled || got != nil {
		t.Fatalf("canceled check = %v,%v", got, err)
	}
	_, dsn := assertionSQLFixture(t)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := checkApplications(context.Background(), tx, "tpcc"); err == nil || !strings.Contains(err.Error(), "unsupported application") {
		t.Fatalf("unsupported application = %v", err)
	}
}
