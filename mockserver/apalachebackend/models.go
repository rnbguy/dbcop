package apalachebackend

import (
	_ "embed"
	"encoding/base64"
)

//go:embed spec/OracleStep.tla
var oracleStep []byte

//go:embed spec/Isolation.tla
var isolation []byte

var (
	encodedOracleStep = base64.StdEncoding.EncodeToString(oracleStep)
	encodedIsolation  = base64.StdEncoding.EncodeToString(isolation)
)

// Sources returns the root module first, followed by its semantic dependency and
// the request-local numeric data module, as required by loadSpec.
func Sources(requestData string) []string {
	return []string{
		encodedOracleStep,
		encodedIsolation,
		base64.StdEncoding.EncodeToString([]byte(requestData)),
	}
}
