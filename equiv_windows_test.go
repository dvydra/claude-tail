//go:build windows

package main

import (
	"testing"
	"time"
)

func runOracleBackfill(t *testing.T, oracle string, fc fixtureCase, d time.Duration) []byte {
	t.Helper()
	t.Skip("bash oracle is not supported on windows")
	return nil
}
