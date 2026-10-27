// Package backend: dbcop session variables.
package backend

import (
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// RegisterVariables installs dbcop session variables. Call once at startup.
//   - dbcop_read_latest (0/1, default 0): when 1, this session selects the
//     newest admitted candidate (writer connections doing read-modify-write).
func RegisterVariables() {
	sql.SystemVariables.AddSystemVariables([]sql.SystemVariable{
		&sql.MysqlSystemVariable{
			Name:    "dbcop_read_latest",
			Scope:   sql.GetMysqlScope(sql.SystemVariableScope_Session),
			Dynamic: true,
			Type:    types.NewSystemBoolType("dbcop_read_latest"),
			Default: int8(0),
		},
	})
}

// readLatestVar reports whether the session set dbcop_read_latest.
func readLatestVar(ctx *sql.Context) bool {
	if ctx == nil || ctx.Session == nil {
		return false
	}
	v, err := ctx.Session.GetSessionVariable(ctx, "dbcop_read_latest")
	if err != nil || v == nil {
		return false
	}
	switch n := v.(type) {
	case int8:
		return n != 0
	case int:
		return n != 0
	case bool:
		return n
	default:
		return false
	}
}
