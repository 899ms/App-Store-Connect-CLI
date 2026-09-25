package cmdtest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	rootcmd "github.com/rudrankriyam/App-Store-Connect-CLI/cmd"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/asc"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
)

type matchExtensionsTarget struct {
	bundleResourceID string
	identifier       string
	profileID        string
	certificates     string
}

func matchExtensionsCertificate(id, serial, content string) string {
	return fmt.Sprintf(`{"type":"certificates","id":%q,"attributes":{"certificateType":"IOS_DISTRIBUTION","serialNumber":%q,"certificateContent":%q,"activated":true,"expirationDate":"2100-01-01T00:00:00Z"}}`, id, serial, content)
}

// startMatchExtensionsStub serves com.app and its extension bundle IDs. The
// bundle ID list mimics App Store Connect's substring filter[identifier] by
// also returning an unrelated com.apple.other and a wildcard com.app.*.
func startMatchExtensionsStub(t *testing.T, targets []matchExtensionsTarget) *atomic.Int32 {
	t.Helper()
	setupAuth(t)

	var mutations atomic.Int32
	byResource := make(map[string]matchExtensionsTarget, len(targets))
	byProfile := make(map[string]matchExtensionsTarget, len(targets))
	bundles := make([]string, 0, len(targets)+2)
	for _, target := range targets {
		byResource[target.bundleResourceID] = target
		byProfile[target.profileID] = target
		bundles = append(bundles, fmt.Sprintf(`{"type":"bundleIds","id":%q,"attributes":{"identifier":%q,"platform":"IOS"}}`, target.bundleResourceID, target.identifier))
	}
	bundles = append(
		bundles,
		`{"type":"bundleIds","id":"bundle-other","attributes":{"identifier":"com.apple.other","platform":"IOS"}}`,
		`{"type":"bundleIds","id":"bundle-wild","attributes":{"identifier":"com.app.*","platform":"IOS"}}`,
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			mutations.Add(1)
			t.Errorf("unexpected mutation: %s %s", req.Method, req.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		switch {
		case req.URL.Path == "/v1/bundleIds":
			if got := req.URL.Query().Get("filter[identifier]"); got != "com.app" {
				t.Errorf("filter[identifier] = %q, want com.app", got)
			}
			writeSigningFetchOutputJSON(t, w, http.StatusOK, `{"data":[`+strings.Join(bundles, ",")+`],"links":{}}`)
		case strings.HasPrefix(req.URL.Path, "/v1/bundleIds/") && strings.HasSuffix(req.URL.Path, "/profiles"):
			resourceID := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/bundleIds/"), "/profiles")
			target, ok := byResource[resourceID]
			if !ok {
				t.Errorf("profiles requested for unmatched bundle %s", resourceID)
				http.Error(w, "unexpected", http.StatusNotFound)
				return
			}
			content := base64.StdEncoding.EncodeToString([]byte("profile-" + target.identifier))
			writeSigningFetchOutputJSON(t, w, http.StatusOK, fmt.Sprintf(
				`{"data":[{"type":"profiles","id":%q,"attributes":{"name":%q,"profileType":"IOS_APP_STORE","profileState":"ACTIVE","expirationDate":"2100-01-01T00:00:00Z","profileContent":%q}}],"links":{}}`,
				target.profileID, "Profile "+target.identifier, content,
			))
		case strings.HasPrefix(req.URL.Path, "/v1/profiles/") && strings.HasSuffix(req.URL.Path, "/certificates"):
			profileID := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/profiles/"), "/certificates")
			target, ok := byProfile[profileID]
			if !ok {
				t.Errorf("certificates requested for unknown profile %s", profileID)
				http.Error(w, "unexpected", http.StatusNotFound)
				return
			}
			writeSigningFetchOutputJSON(t, w, http.StatusOK, `{"data":[`+target.certificates+`],"links":{}}`)
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.String())
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	transport := server.Client().Transport
	client, err := asc.NewClientWithHTTPClient(
		os.Getenv("ASC_KEY_ID"),
		os.Getenv("ASC_ISSUER_ID"),
		os.Getenv("ASC_PRIVATE_KEY_PATH"),
		&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			cloned := req.Clone(req.Context())
			cloned.URL.Scheme = serverURL.Scheme
			cloned.URL.Host = serverURL.Host
			return transport.RoundTrip(cloned)
		})},
	)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	t.Cleanup(shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) {
		return client, nil
	}))
	return &mutations
}

func runMatchExtensionsFetch(t *testing.T, outputDir, format string) (int, string, string) {
	t.Helper()
	var code int
	stdout, stderr := captureOutput(t, func() {
		code = rootcmd.Run([]string{
			"signing", "fetch",
			"--bundle-id", "com.app",
			"--profile-type", "IOS_APP_STORE",
			"--match-extensions",
			"--output", outputDir,
			"--format", format,
		}, "test")
	})
	return code, stdout, stderr
}

type matchExtensionsReceipt struct {
	MatchedBundleIDs []string `json:"matchedBundleIds"`
	Results          []struct {
		BundleID         string   `json:"bundleId"`
		CertificateFiles []string `json:"certificateFiles"`
		ProfileFile      string   `json:"profileFile"`
	} `json:"results"`
	Failures []struct {
		BundleID string `json:"bundleId"`
		Error    string `json:"error"`
	} `json:"failures"`
}

func TestSigningFetchMatchExtensionsSharesCertificateAcrossTargets(t *testing.T) {
	sharedCert := matchExtensionsCertificate("cert-1", "SER1", base64.StdEncoding.EncodeToString([]byte("cert-one")))
	mutations := startMatchExtensionsStub(t, []matchExtensionsTarget{
		{bundleResourceID: "bundle-app", identifier: "com.app", profileID: "profile-app", certificates: sharedCert},
		{bundleResourceID: "bundle-widget", identifier: "com.app.widget", profileID: "profile-widget", certificates: sharedCert},
		{bundleResourceID: "bundle-clip", identifier: "com.app.clip", profileID: "profile-clip", certificates: sharedCert},
	})
	outputDir := t.TempDir()

	code, stdout, stderr := runMatchExtensionsFetch(t, outputDir, "json")
	if code != rootcmd.ExitSuccess {
		t.Fatalf("exit code = %d, stderr = %q, stdout = %q", code, stderr, stdout)
	}
	var receipt matchExtensionsReceipt
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if got := strings.Join(receipt.MatchedBundleIDs, ","); got != "com.app,com.app.widget,com.app.clip" {
		t.Fatalf("matchedBundleIds = %q; the wildcard and com.apple.other must not match", got)
	}
	if len(receipt.Failures) != 0 || len(receipt.Results) != 3 {
		t.Fatalf("receipt = %+v, want 3 results and no failures", receipt)
	}
	certPath := filepath.Join(outputDir, "SER1.cer")
	for _, result := range receipt.Results {
		if len(result.CertificateFiles) != 1 || result.CertificateFiles[0] != certPath {
			t.Fatalf("%s certificateFiles = %v, want [%s]", result.BundleID, result.CertificateFiles, certPath)
		}
		if _, err := os.Stat(result.ProfileFile); err != nil {
			t.Fatalf("%s profile file: %v", result.BundleID, err)
		}
	}
	if data, err := os.ReadFile(certPath); err != nil || string(data) != "cert-one" {
		t.Fatalf("shared certificate = %q, %v", data, err)
	}
	if got := mutations.Load(); got != 0 {
		t.Fatalf("mutations = %d, want 0", got)
	}
}

func TestSigningFetchMatchExtensionsFailedTargetKeepsSharedCertificate(t *testing.T) {
	sharedCert := matchExtensionsCertificate("cert-1", "SER1", base64.StdEncoding.EncodeToString([]byte("cert-one")))
	badCert := matchExtensionsCertificate("cert-2", "SER2", "!!not-base64!!")
	startMatchExtensionsStub(t, []matchExtensionsTarget{
		{bundleResourceID: "bundle-app", identifier: "com.app", profileID: "profile-app", certificates: sharedCert},
		{bundleResourceID: "bundle-widget", identifier: "com.app.widget", profileID: "profile-widget", certificates: sharedCert + "," + badCert},
	})
	outputDir := t.TempDir()

	code, stdout, _ := runMatchExtensionsFetch(t, outputDir, "json")
	if code == rootcmd.ExitSuccess {
		t.Fatal("expected a failure exit code when one target fails")
	}
	var receipt matchExtensionsReceipt
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if len(receipt.Results) != 1 || receipt.Results[0].BundleID != "com.app" {
		t.Fatalf("results = %+v, want only com.app", receipt.Results)
	}
	if len(receipt.Failures) != 1 || receipt.Failures[0].BundleID != "com.app.widget" || receipt.Failures[0].Error == "" {
		t.Fatalf("failures = %+v, want com.app.widget", receipt.Failures)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "SER1.cer")); err != nil {
		t.Fatalf("shared certificate removed by the failed target: %v", err)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "widget") {
			t.Fatalf("failed target left %s behind", entry.Name())
		}
	}
}

func TestSigningFetchMatchExtensionsTableOutputIsATable(t *testing.T) {
	sharedCert := matchExtensionsCertificate("cert-1", "SER1", base64.StdEncoding.EncodeToString([]byte("cert-one")))
	startMatchExtensionsStub(t, []matchExtensionsTarget{
		{bundleResourceID: "bundle-app", identifier: "com.app", profileID: "profile-app", certificates: sharedCert},
		{bundleResourceID: "bundle-widget", identifier: "com.app.widget", profileID: "profile-widget", certificates: sharedCert},
	})

	code, stdout, stderr := runMatchExtensionsFetch(t, t.TempDir(), "table")
	if code != rootcmd.ExitSuccess {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") || !strings.Contains(stdout, "com.app.widget") || !strings.Contains(stdout, "Bundle ID") {
		t.Fatalf("table output = %q, want a rendered table", stdout)
	}
}

func TestSigningFetchMatchExtensionsRejectsCreateMissingCertificate(t *testing.T) {
	var code int
	stdout, stderr := captureOutput(t, func() {
		code = rootcmd.Run([]string{
			"signing", "fetch",
			"--bundle-id", "com.app",
			"--profile-type", "IOS_APP_STORE",
			"--match-extensions",
			"--create-missing",
			"--create-missing-certificate",
			"--identity-password-file", "password",
			"--output", t.TempDir(),
		}, "test")
	})
	if code != rootcmd.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, rootcmd.ExitUsage)
	}
	if stdout != "" || !strings.Contains(stderr, "--match-extensions cannot be combined with --create-missing-certificate") {
		t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
	}
}

func TestSigningSyncPushRejectsMatchExtensionsWithTargetsFile(t *testing.T) {
	var code int
	stdout, stderr := captureOutput(t, func() {
		code = rootcmd.Run([]string{
			"signing", "sync", "push",
			"--targets-file", "targets.json",
			"--match-extensions",
			"--profile-type", "IOS_APP_STORE",
			"--repo", "git@example.com:org/signing.git",
		}, "test")
	})
	if code != rootcmd.ExitUsage {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, rootcmd.ExitUsage, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "--match-extensions requires --bundle-id") {
		t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
	}
}
