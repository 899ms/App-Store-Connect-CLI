package cmdtest

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
)

// isolateAuthTokenProfile keeps the root --profile override from leaking
// between tests because the selected profile is process-global state.
func isolateAuthTokenProfile(t *testing.T) {
	t.Helper()
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("ASC_BYPASS_KEYCHAIN", "1")
	t.Setenv("ASC_PROFILE", "")
	setCmdtestHome(t)

	previousProfile := shared.SelectedProfile()
	shared.SetSelectedProfile("")
	t.Cleanup(func() {
		shared.SetSelectedProfile(previousProfile)
	})
}

func TestAuthTokenMissingConfirmPrintsExactReinvocation(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantRerun string
	}{
		{
			name:      "bare invocation",
			args:      []string{"auth", "token"},
			wantRerun: "asc auth token --confirm",
		},
		{
			name:      "preserves root profile flag",
			args:      []string{"--profile", "release", "auth", "token"},
			wantRerun: "asc --profile release auth token --confirm",
		},
		{
			name:      "preserves root strict auth flag",
			args:      []string{"--strict-auth", "auth", "token"},
			wantRerun: "asc --strict-auth auth token --confirm",
		},
		{
			name:      "preserves command flags",
			args:      []string{"--profile", "release", "auth", "token", "--name", "MyKey", "--output", "json", "--pretty"},
			wantRerun: "asc --profile release auth token --name MyKey --output json --pretty --confirm",
		},
		{
			name:      "rewrites explicit confirm false",
			args:      []string{"auth", "token", "--confirm=false", "--output", "json"},
			wantRerun: "asc auth token --output json --confirm",
		},
		{
			name:      "shell-quotes values so the printed command cannot expand",
			args:      []string{"--profile", "$(whoami) key", "auth", "token", "--name", "it's mine"},
			wantRerun: `asc --profile '$(whoami) key' auth token --name 'it'\''s mine' --confirm`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			isolateAuthTokenProfile(t)

			root := RootCommand("1.2.3")
			root.FlagSet.SetOutput(io.Discard)

			var runErr error
			stdout, stderr := captureOutput(t, func() {
				if err := root.Parse(test.args); err != nil {
					t.Fatalf("parse error: %v", err)
				}
				runErr = root.Run(context.Background())
			})

			if !errors.Is(runErr, flag.ErrHelp) {
				t.Fatalf("expected flag.ErrHelp usage error, got %v", runErr)
			}
			if kind := shared.ClassifyUsageError(runErr); kind != shared.UsageErrorMissingRequired {
				t.Fatalf("usage error kind = %q, want %q", kind, shared.UsageErrorMissingRequired)
			}
			if stdout != "" {
				t.Fatalf("expected empty stdout, got %q", stdout)
			}
			// The reason line and the exact re-invocation must lead stderr, ahead
			// of the usage page ffcli renders for flag.ErrHelp.
			wantBlock := "Error: --confirm is required because `asc auth token` prints a live bearer token to stdout, " +
				"where it can leak into shell history, logs, or CI output\n" +
				"Re-run: " + test.wantRerun + "\n"
			if !strings.HasPrefix(stderr, wantBlock) {
				t.Fatalf("stderr = %q, want prefix %q", stderr, wantBlock)
			}
		})
	}
}
