package backend

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
)

func TestIsolationConflictCommitWireRollback(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	store := NewStore(1, 2)
	t.Cleanup(store.Close)
	store.SetSchedule([]int{0})

	// T1 reads initial y, then writes x. T2's initial x read is legal
	// until its write to y closes a serializability dependency cycle.
	first := NewSession(store, Serializable, 1000)
	t.Cleanup(first.SessionEnd)
	tx := beginSessionTransaction(t, first)
	if _, version, ok := first.oracleRead("items", "1", "y"); !ok || version != 0 {
		t.Fatal("first transaction could not read initial y")
	}
	first.liveTxn().stage(StagedWrite{Tab: "items", Pkey: "1", Col: "x", Value: int64(1)})
	if err := first.CommitTransaction(nil, tx); err != nil {
		t.Fatalf("valid first commit failed: %v", err)
	}
	assertSessionTransactionEnded(t, first)

	RegisterVariables()
	engine := NewEngine(NewProvider(store, Serializable, nil))
	t.Cleanup(func() { _ = engine.Close() })
	built := make(chan *Session, 1)
	builder := SessionBuilder(store, Serializable)
	srv, err := server.NewServer(server.Config{Protocol: "tcp", Address: "127.0.0.1:0"}, engine, sql.NewContext,
		func(ctx context.Context, conn *mysql.Conn, addr string) (sql.Session, error) {
			session, err := builder(ctx, conn, addr)
			if err == nil {
				built <- session.(*Session)
			}
			return session, err
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	serving := make(chan error, 1)
	go func() { serving <- srv.Start() }()
	t.Cleanup(func() {
		_ = srv.Close()
		<-serving
		srv.SessionManager().WaitForClosedConnections()
	})
	host, portString, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := mysql.Connect(ctx, &mysql.ConnParams{Host: host, Port: port, Uname: "root"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	if _, err := conn.ExecuteFetch("BEGIN", 0, false); err != nil {
		t.Fatal(err)
	}
	var second *Session
	select {
	case second = <-built:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if second.liveTxn() == nil {
		t.Fatal("wire BEGIN did not start backend transaction")
	}
	if _, version, ok := second.oracleRead("items", "1", "x"); !ok || version != 0 {
		t.Fatal("valid stale read was not admitted before the conflicting write")
	}
	second.liveTxn().stage(StagedWrite{Tab: "items", Pkey: "1", Col: "y", Value: int64(1)})
	second.liveTxn().stageMembership("items", "2", true)
	_, err = conn.ExecuteFetch("COMMIT", 0, false)
	var wireErr *mysql.SQLError
	if !errors.As(err, &wireErr) || wireErr.Number() != 1213 || wireErr.SQLState() != "40001" {
		t.Fatalf("isolation rejection must reach client as 1213/40001, got %v", err)
	}
	assertSessionTransactionEnded(t, second)
	if logged := store.logSnapshot(); len(logged) != 1 || logged[0].Session != first.sid {
		t.Fatalf("rejected transaction leaked committed history: %+v", logged)
	}
	if len(store.committedVersions(cellKey("items", "1", "x"))) != 1 ||
		len(store.committedVersions(cellKey("items", "1", "y"))) != 0 ||
		len(store.committedVersions(cellKey("items", "2", rowExistenceColumn))) != 0 {
		t.Fatal("rejected transaction leaked versions or erased the valid commit")
	}
	if _, err := conn.ExecuteFetch("BEGIN", 0, false); err != nil {
		t.Fatalf("connection cannot start a fresh transaction after rollback: %v", err)
	}
	if _, err := conn.ExecuteFetch("COMMIT", 0, false); err != nil {
		t.Fatalf("fresh valid transaction failed after rollback: %v", err)
	}
	assertSessionTransactionEnded(t, second)
}
