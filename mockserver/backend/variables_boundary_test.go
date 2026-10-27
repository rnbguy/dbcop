package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

type policyValueSession struct {
	sql.Session
	value interface{}
	err   error
}

func (s *policyValueSession) GetSessionVariable(*sql.Context, string) (interface{}, error) {
	return s.value, s.err
}

func TestReadLatestPolicyRejectsUnavailableOrUnsupportedValues(t *testing.T) {
	if readLatestVar(nil) {
		t.Fatal("a missing context enabled latest selection")
	}
	for _, test := range []struct {
		name  string
		value interface{}
		err   error
		want  bool
	}{
		{"int8_enabled", int8(1), nil, true},
		{"int8_disabled", int8(0), nil, false},
		{"integer_enabled", int(1), nil, true},
		{"integer_disabled", int(0), nil, false},
		{"boolean_enabled", true, nil, true},
		{"boolean_disabled", false, nil, false},
		{"missing", nil, nil, false},
		{"unsupported_text", "1", nil, false},
		{"lookup_error_wins", true, errors.New("session variable unavailable"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &policyValueSession{Session: sql.NewBaseSession(), value: test.value, err: test.err}
			ctx := sql.NewContext(context.Background(), sql.WithSession(session))
			if got := readLatestVar(ctx); got != test.want {
				t.Fatalf("latest selection for value %v and error %v = %v, want %v", test.value, test.err, got, test.want)
			}
		})
	}
}
