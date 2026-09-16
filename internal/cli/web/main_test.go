package web

import (
	"os"
	"testing"
)

// TestMain isolates the web session cache so package tests that exercise the
// real store resolvers never read or write the developer's own cached
// sessions or open the native keychain.
func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "asc-web-cli-test-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", tempDir)
	_ = os.Setenv("ASC_BYPASS_KEYCHAIN", "1")
	_ = os.Setenv("ASC_WEB_SESSION_CACHE_DIR", tempDir)
	_ = os.Setenv("ASC_WEB_SESSION_CACHE_BACKEND", "file")

	code := m.Run()

	_ = os.RemoveAll(tempDir)
	os.Exit(code)
}
