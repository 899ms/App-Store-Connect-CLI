package cmdtest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rudrankriyam/App-Store-Connect-CLI/cmd"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/asc"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
)

// The relationship types each links command accepts, in the order the CLI
// prints them.
var (
	buildRelationshipTypeValues = []string{
		"app",
		"appStoreVersion",
		"betaBuildLocalizations",
		"buildBetaDetail",
		"diagnosticSignatures",
		"icons",
		"individualTesters",
		"preReleaseVersion",
	}
	betaGroupRelationshipTypeValues  = []string{"betaTesters", "builds"}
	betaTesterRelationshipTypeValues = []string{"apps", "betaGroups", "builds"}
	preReleaseRelationshipTypeValues = []string{"app", "builds"}
)

// notFoundBody renders Apple's 404 for a resource type and ID. Captured live
// from GET /v1/builds/999999999999/relationships/app on 2026-09-15.
func notFoundBody(resourceType, id string) string {
	return `{"errors":[{"id":"2f4a6c8e-0b1d-4e3f-8a9b-0c1d2e3f4a5b","status":"404","code":"NOT_FOUND","title":"The specified resource does not exist","detail":"There is no resource of type '` + resourceType + `' with id '` + id + `'"}]}`
}

func TestLinksTypeUsageErrorsEnumerateValidValues(t *testing.T) {
	t.Setenv("ASC_APP_ID", "")

	tests := []struct {
		name   string
		base   []string
		values []string
	}{
		{
			name:   "builds links view",
			base:   []string{"builds", "links", "view", "--build-id", "build-1"},
			values: buildRelationshipTypeValues,
		},
		{
			name:   "testflight groups links view",
			base:   []string{"testflight", "groups", "links", "view", "--group-id", "group-1"},
			values: betaGroupRelationshipTypeValues,
		},
		{
			name:   "testflight testers links view",
			base:   []string{"testflight", "testers", "links", "view", "--tester-id", "tester-1"},
			values: betaTesterRelationshipTypeValues,
		},
		{
			name:   "testflight pre-release links view",
			base:   []string{"testflight", "pre-release", "links", "view", "--id", "pr-1"},
			values: preReleaseRelationshipTypeValues,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cases := []struct {
				name       string
				args       []string
				wantPrefix string
			}{
				{
					name:       "missing type",
					args:       test.base,
					wantPrefix: "Error: --type is required; must be one of: " + strings.Join(test.values, ", "),
				},
				{
					name:       "invalid type",
					args:       append(append([]string{}, test.base...), "--type", "notARelationship"),
					wantPrefix: `Error: --type "notARelationship" is not a valid relationship type; must be one of: ` + strings.Join(test.values, ", "),
				},
			}

			for _, testCase := range cases {
				t.Run(testCase.name, func(t *testing.T) {
					root := RootCommand("1.2.3")
					root.FlagSet.SetOutput(io.Discard)

					var runErr error
					stdout, stderr := captureOutput(t, func() {
						if err := root.Parse(testCase.args); err != nil {
							t.Fatalf("parse error: %v", err)
						}
						runErr = root.Run(context.Background())
					})

					if got := cmd.ExitCodeFromError(runErr); got != cmd.ExitUsage {
						t.Fatalf("exit code = %d, want %d (%v)", got, cmd.ExitUsage, runErr)
					}
					if stdout != "" {
						t.Fatalf("stdout = %q, want empty", stdout)
					}
					if !strings.HasPrefix(stderr, testCase.wantPrefix) {
						t.Fatalf("stderr = %q, want prefix %q", stderr, testCase.wantPrefix)
					}
				})
			}
		})
	}
}

func TestLinksTypeHelpListsValidValues(t *testing.T) {
	tests := []struct {
		name   string
		path   []string
		values []string
	}{
		{
			name:   "builds links view",
			path:   []string{"builds", "links", "view"},
			values: buildRelationshipTypeValues,
		},
		{
			name:   "testflight groups links view",
			path:   []string{"testflight", "groups", "links", "view"},
			values: betaGroupRelationshipTypeValues,
		},
		{
			name:   "testflight testers links view",
			path:   []string{"testflight", "testers", "links", "view"},
			values: betaTesterRelationshipTypeValues,
		},
		{
			name:   "testflight pre-release links view",
			path:   []string{"testflight", "pre-release", "links", "view"},
			values: preReleaseRelationshipTypeValues,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := "Relationship type (required); must be one of: " + strings.Join(test.values, ", ")
			if usage := usageForCommand(t, test.path...); !strings.Contains(usage, want) {
				t.Fatalf("usage = %q, want it to contain %q", usage, want)
			}
		})
	}
}

func TestLinksNotFoundNamesMissingResource(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		path       string
		body       string
		wantErr    string
		wantPrefix string
	}{
		{
			name:    "builds links unknown build",
			args:    []string{"builds", "links", "view", "--build-id", "999999999999", "--type", "app", "--output", "json"},
			path:    "/v1/builds/999999999999/relationships/app",
			body:    notFoundBody("builds", "999999999999"),
			wantErr: `builds links view: build "999999999999" was not found; --build-id expects a build ID (list them with: asc builds list --app "APP_ID")`,
		},
		{
			name:       "builds links missing relationship",
			args:       []string{"builds", "links", "view", "--build-id", "build-1", "--type", "buildBetaDetail", "--output", "json"},
			path:       "/v1/builds/build-1/relationships/buildBetaDetail",
			body:       notFoundBody("buildBetaDetails", "build-1"),
			wantPrefix: `builds links view: buildBetaDetail relationship was not found for build "build-1"`,
		},
		{
			name:    "builds links next url",
			args:    []string{"builds", "links", "view", "--type", "individualTesters", "--next", "https://api.appstoreconnect.apple.com/v1/builds/other-build/relationships/individualTesters?cursor=NEXT", "--output", "json"},
			path:    "/v1/builds/other-build/relationships/individualTesters",
			body:    notFoundBody("builds", "other-build"),
			wantErr: "builds links view: the build referenced by the requested page URL was not found",
		},
		{
			name:    "builds links unrelated not found keeps api message",
			args:    []string{"builds", "links", "view", "--build-id", "build-1", "--type", "app", "--output", "json"},
			path:    "/v1/builds/build-1/relationships/app",
			body:    notFoundBody("appStoreVersions", "version-1"),
			wantErr: "builds links view: The specified resource does not exist: There is no resource of type 'appStoreVersions' with id 'version-1'",
		},
		{
			name:    "testflight groups links unknown group",
			args:    []string{"testflight", "groups", "links", "view", "--group-id", "group-404", "--type", "betaTesters", "--output", "json"},
			path:    "/v1/betaGroups/group-404/relationships/betaTesters",
			body:    notFoundBody("betaGroups", "group-404"),
			wantErr: `testflight groups links view: group "group-404" was not found; --group-id expects a group ID (list them with: asc testflight groups list --app "APP_ID")`,
		},
		{
			name:       "testflight groups links missing relationship",
			args:       []string{"testflight", "groups", "links", "view", "--group-id", "group-1", "--type", "betaTesters", "--output", "json"},
			path:       "/v1/betaGroups/group-1/relationships/betaTesters",
			body:       notFoundBody("betaTesters", "group-1"),
			wantPrefix: `testflight groups links view: betaTesters relationship was not found for group "group-1"`,
		},
		{
			name:    "testflight testers links unknown tester",
			args:    []string{"testflight", "testers", "links", "view", "--tester-id", "tester-404", "--type", "apps", "--output", "json"},
			path:    "/v1/betaTesters/tester-404/relationships/apps",
			body:    notFoundBody("betaTesters", "tester-404"),
			wantErr: `testflight testers links view: tester "tester-404" was not found; --tester-id expects a tester ID (list them with: asc testflight testers list --app "APP_ID")`,
		},
		{
			name:       "testflight testers links missing relationship",
			args:       []string{"testflight", "testers", "links", "view", "--tester-id", "tester-1", "--type", "betaGroups", "--output", "json"},
			path:       "/v1/betaTesters/tester-1/relationships/betaGroups",
			body:       notFoundBody("betaGroups", "tester-1"),
			wantPrefix: `testflight testers links view: betaGroups relationship was not found for tester "tester-1"`,
		},
		{
			name:    "testflight pre-release links unknown version",
			args:    []string{"testflight", "pre-release", "links", "view", "--id", "pr-404", "--type", "app", "--output", "json"},
			path:    "/v1/preReleaseVersions/pr-404/relationships/app",
			body:    notFoundBody("preReleaseVersions", "pr-404"),
			wantErr: `testflight pre-release links view: pre-release version "pr-404" was not found; --id expects a pre-release version ID (list them with: asc testflight pre-release list --app "APP_ID")`,
		},
		{
			name:       "testflight pre-release links missing relationship",
			args:       []string{"testflight", "pre-release", "links", "view", "--id", "pr-1", "--type", "builds", "--output", "json"},
			path:       "/v1/preReleaseVersions/pr-1/relationships/builds",
			body:       notFoundBody("builds", "pr-1"),
			wantPrefix: `testflight pre-release links view: builds relationship was not found for pre-release version "pr-1"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setupAuth(t)
			t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "nonexistent.json"))
			server := newNotFoundServer(t, test.path, test.body)
			useLinksServerClient(t, server)

			root := RootCommand("1.2.3")
			root.FlagSet.SetOutput(io.Discard)

			var runErr error
			stdout, _ := captureOutput(t, func() {
				if err := root.Parse(test.args); err != nil {
					t.Fatalf("parse error: %v", err)
				}
				runErr = root.Run(context.Background())
			})

			if runErr == nil {
				t.Fatal("expected not-found error")
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if got := cmd.ExitCodeFromError(runErr); got != cmd.ExitNotFound {
				t.Fatalf("exit code = %d, want %d (%v)", got, cmd.ExitNotFound, runErr)
			}
			if test.wantErr != "" && runErr.Error() != test.wantErr {
				t.Fatalf("error = %q, want %q", runErr, test.wantErr)
			}
			if test.wantPrefix != "" && !strings.HasPrefix(runErr.Error(), test.wantPrefix) {
				t.Fatalf("error = %q, want prefix %q", runErr, test.wantPrefix)
			}
		})
	}
}

// useLinksServerClient points the shared command client factory at the test
// server so the links commands exercise real transport and error parsing.
func useLinksServerClient(t *testing.T, server *httptest.Server) {
	t.Helper()

	client, err := asc.NewClientWithHTTPClient(
		"TEST_KEY",
		"TEST_ISSUER",
		os.Getenv("ASC_PRIVATE_KEY_PATH"),
		&http.Client{Transport: serverRoundTripper(t, server)},
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	restore := shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) {
		return client, nil
	})
	t.Cleanup(restore)
}
