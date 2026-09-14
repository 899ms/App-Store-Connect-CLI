//go:build !darwin

package cmdtest

import (
	"context"
	"io"
	"strings"
	"testing"

	rootcmd "github.com/rudrankriyam/App-Store-Connect-CLI/cmd"
)

func TestSigningRunUnsupportedPlatformRendersStderrAndUsageExit(t *testing.T) {
	root := RootCommand("test")
	root.FlagSet.SetOutput(io.Discard)
	args := []string{
		"signing", "run",
		"--identity", "identity.p12",
		"--profile", "profile.mobileprovision",
		"--", "child-tool", "--child-flag",
	}

	var runErr error
	stdout, stderr := captureOutput(t, func() {
		if err := root.Parse(args); err != nil {
			t.Fatalf("parse error: %v", err)
		}
		runErr = root.Run(context.Background())
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "supported only on macOS") {
		t.Fatalf("error = %v, want unsupported-platform diagnostic", runErr)
	}
	if got := rootcmd.ExitCodeFromError(runErr); got != rootcmd.ExitUsage {
		t.Fatalf("exit code = %d, want %d", got, rootcmd.ExitUsage)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "Error: signing run is supported only on macOS") {
		t.Fatalf("stderr = %q, want platform diagnostic", stderr)
	}
}
