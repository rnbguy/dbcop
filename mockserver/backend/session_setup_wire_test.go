package backend

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
)

func TestEmptyTransactionSetupDoesNotBlockOtherConnections(t *testing.T) {
	store := NewStore(1, 2)
	t.Cleanup(store.Close)
	RegisterVariables()
	engine := NewEngine(NewProvider(store, Serializable, nil))
	t.Cleanup(func() { _ = engine.Close() })
	srv, err := server.NewServer(server.Config{Protocol: "tcp", Address: "127.0.0.1:0"}, engine,
		sql.NewContext, SessionBuilder(store, Serializable), nil)
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
	connect := func() *mysql.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := mysql.Connect(ctx, &mysql.ConnParams{Host: host, Port: port, Uname: "root"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(conn.Close)
		return conn
	}
	first := connect()
	for _, query := range []string{"CREATE DATABASE setup", "USE setup", "CREATE TABLE records (id INT PRIMARY KEY)", "SET autocommit=0"} {
		if _, err := first.ExecuteFetch(query, 0, false); err != nil {
			t.Fatal(err)
		}
	}
	second := connect()
	if err := second.Conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ExecuteFetch("SELECT @@autocommit", 1, true); err != nil {
		t.Fatalf("empty JDBC transaction setup blocked another connection: %v", err)
	}
	for _, query := range []string{"INSERT INTO records VALUES (1)", "ROLLBACK"} {
		if _, err := first.ExecuteFetch(query, 0, false); err != nil {
			t.Fatal(err)
		}
	}
	result, err := second.ExecuteFetch("SELECT COUNT(*) FROM setup.records", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0][0].ToString() != "0" {
		t.Fatalf("setup lock release accidentally committed the user transaction: %v", result.Rows)
	}
}
