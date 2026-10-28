//go:build cgo

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/dolthub/vitess/go/mysql"
)

// Only the selected failure is injected; successful results come from the real engine.
type assertionFailingQueryHandler struct {
	mysql.Handler
	query string
	after bool
}

func (h assertionFailingQueryHandler) ComQuery(ctx context.Context, conn *mysql.Conn, query string, callback mysql.ResultSpoolFn) error {
	if !strings.EqualFold(strings.TrimSpace(query), h.query) {
		return h.Handler.ComQuery(ctx, conn, query, callback)
	}
	if h.after {
		if err := h.Handler.ComQuery(ctx, conn, query, callback); err != nil {
			return err
		}
		if err := conn.FlushBuffer(); err != nil {
			return err
		}
	}
	return mysql.NewSQLError(1105, "HY000", "injected assertion transport failure")
}

func TestCheckSQLWireFailuresRemainExecutionErrors(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		after       bool
	}{
		{"latest strategy", "SET @@session.dbcop_read_latest = 1", false},
		{"begin", "START TRANSACTION", false},
		{"rollback", "ROLLBACK", false},
		{"count stream", "SELECT custid FROM accounts", true},
		{"balance stream", "SELECT custid, bal FROM savings", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, dsn := assertionSQLFixtureWithHandler(t, func(h mysql.Handler) (mysql.Handler, error) {
				return assertionFailingQueryHandler{Handler: h, query: tc.query, after: tc.after}, nil
			}, "CREATE TABLE accounts (custid BIGINT)", "CREATE TABLE savings (custid BIGINT, bal VARCHAR(80))",
				"CREATE TABLE checking (custid BIGINT, bal VARCHAR(80))", "INSERT INTO accounts VALUES (1)",
				"INSERT INTO savings VALUES (1,'2500')", "INSERT INTO checking VALUES (1,'2500')")
			result, err := check(context.Background(), dsn, "smallbank")
			if err == nil || result != nil || (!tc.after && !strings.Contains(err.Error(), "injected assertion transport failure")) || (tc.after && !strings.Contains(err.Error(), tc.query)) {
				t.Fatalf("failed %s became result %v, error %v", tc.name, result, err)
			}
		})
	}
	t.Run("user stream", func(t *testing.T) {
		_, dsn := assertionSQLFixtureWithHandler(t, func(h mysql.Handler) (mysql.Handler, error) {
			return assertionFailingQueryHandler{Handler: h, query: "SELECT user_id, user_editcount FROM useracct", after: true}, nil
		}, "CREATE TABLE useracct (user_id BIGINT, user_editcount BIGINT)", "INSERT INTO useracct VALUES (1,0)")
		if result, err := check(context.Background(), dsn, "wikipedia"); err == nil || result != nil || !strings.Contains(err.Error(), "SELECT user_id, user_editcount FROM useracct") {
			t.Fatalf("failed user stream became result %v, error %v", result, err)
		}
	})
	t.Run("TPCC stream", func(t *testing.T) {
		_, dsn := assertionSQLFixtureWithHandler(t, func(h mysql.Handler) (mysql.Handler, error) {
			return assertionFailingQueryHandler{Handler: h, query: "SELECT W_ID, W_YTD FROM WAREHOUSE", after: true}, nil
		}, "CREATE TABLE WAREHOUSE (W_ID BIGINT, W_YTD VARCHAR(80))", "INSERT INTO WAREHOUSE VALUES (1,'60')")
		if result, err := check(context.Background(), dsn, "tpcc"); err == nil || result != nil || !strings.Contains(err.Error(), "SELECT W_ID, W_YTD FROM WAREHOUSE") {
			t.Fatalf("failed TPCC stream became result %v, error %v", result, err)
		}
	})
}
