package apalachebackend

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestCallNullPresenceAndNestedDuplicates(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"null result", `{"jsonrpc":"2.0","id":2,"result":null}`, true},
		{"null result and null error", `{"jsonrpc":"2.0","id":2,"result":null,"error":null}`, false},
		{"object result and null error", `{"jsonrpc":"2.0","id":2,"result":{},"error":null}`, false},
		{"nested duplicate result", `{"jsonrpc":"2.0","id":2,"result":{"source":{"version":1,"version":2}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := transportClient(t, func(w http.ResponseWriter, _ *http.Request, _ transportRequest) {
				_, _ = io.WriteString(w, tc.body)
			})
			if err := client.Call(context.Background(), "transportProbe", nil, nil); (err == nil) != tc.ok {
				t.Fatalf("response acceptance=%v, want %v: %v", err == nil, tc.ok, err)
			}
		})
	}
}
