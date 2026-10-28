// Package backend: session builder wiring mock sessions to connections.
package backend

import (
	"context"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
)

// SessionBuilder returns a server.SessionBuilder binding each connection
// to a Session over store at isolation iso. The MySQL connection id is
// the dbcop session identity: distinct connections stay distinct.
func SessionBuilder(store *Store, iso Isolation) func(ctx context.Context, conn *mysql.Conn, addr string) (sql.Session, error) {
	return func(_ context.Context, conn *mysql.Conn, addr string) (sql.Session, error) {
		host, user := "", ""
		if u, ok := conn.UserData.(sql.MysqlConnectionUser); ok {
			host, user = u.Host, u.User
		}
		client := sql.Client{Address: host, User: user, Capabilities: conn.Capabilities}
		base := sql.NewBaseSessionWithClientServer(addr, client, conn.ConnectionID)
		return &Session{BaseSession: base, store: store, iso: iso, sid: int(conn.ConnectionID)}, nil
	}
}
