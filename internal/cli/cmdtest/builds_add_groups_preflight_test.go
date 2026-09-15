package cmdtest

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rootcmd "github.com/rudrankriyam/App-Store-Connect-CLI/cmd"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
)

// addGroupsPreflightFixture serves the requests that precede the beta-group
// POST so each precondition case can assert on state handling alone.
type addGroupsPreflightFixture struct {
	t *testing.T

	betaGroups      string
	build           string
	buildBetaDetail string

	buildStatus           int
	buildBetaDetailStatus int

	postStatus int
	postBody   string

	// buildReadDelay slows the build state read so the preflight budget can
	// expire before it returns.
	buildReadDelay time.Duration

	buildReads           int
	buildBetaDetailReads int
	postCount            int
}

func (f *addGroupsPreflightFixture) transport() roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1/builds/build-1/app":
			return jsonResponse(http.StatusOK, `{"data":{"type":"apps","id":"app-1"}}`)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/apps/app-1/betaGroups":
			return jsonResponse(http.StatusOK, f.betaGroups)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/builds/build-1":
			f.buildReads++
			if f.buildReadDelay > 0 {
				select {
				case <-time.After(f.buildReadDelay):
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			}
			status := f.buildStatus
			if status == 0 {
				status = http.StatusOK
			}
			return jsonResponse(status, f.build)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/builds/build-1/buildBetaDetail":
			f.buildBetaDetailReads++
			status := f.buildBetaDetailStatus
			if status == 0 {
				status = http.StatusOK
			}
			return jsonResponse(status, f.buildBetaDetail)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/builds/build-1/relationships/betaGroups":
			f.postCount++
			status := f.postStatus
			if status == 0 {
				status = http.StatusNoContent
			}
			return jsonResponse(status, f.postBody)
		default:
			f.t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	}
}

const (
	addGroupsExternalGroupPayload = `{"data":[{"type":"betaGroups","id":"group-external","attributes":{"name":"External QA","isInternalGroup":false}}]}`
	addGroupsInternalGroupPayload = `{"data":[{"type":"betaGroups","id":"group-internal","attributes":{"name":"Friends & Family","isInternalGroup":true}}]}`
	addGroupsValidBuildPayload    = `{"data":{"type":"builds","id":"build-1","attributes":{"version":"42","processingState":"VALID","expired":false,"buildAudienceType":"APP_STORE_ELIGIBLE"}}}`
)

func runAddGroupsPreflight(t *testing.T, fixture *addGroupsPreflightFixture, args ...string) (string, string, error) {
	t.Helper()
	setupAuth(t)
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "nonexistent.json"))

	originalTransport := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})
	http.DefaultTransport = fixture.transport()

	root := RootCommand("1.2.3")
	root.FlagSet.SetOutput(io.Discard)

	var runErr error
	stdout, stderr := captureOutput(t, func() {
		if err := root.Parse(args); err != nil {
			t.Fatalf("parse error: %v", err)
		}
		runErr = root.Run(context.Background())
	})
	return stdout, stderr, runErr
}

func TestBuildsAddGroupsPreflightBlocksProcessingBuild(t *testing.T) {
	fixture := &addGroupsPreflightFixture{
		t:          t,
		betaGroups: addGroupsExternalGroupPayload,
		build:      `{"data":{"type":"builds","id":"build-1","attributes":{"version":"42","processingState":"PROCESSING","expired":false}}}`,
	}

	_, stderr, runErr := runAddGroupsPreflight(t, fixture,
		"builds", "add-groups", "--build-id", "build-1", "--group", "group-external")

	if runErr == nil {
		t.Fatalf("expected preflight failure, got nil error")
	}
	if fixture.postCount != 0 {
		t.Fatalf("expected no beta-group POST, got %d", fixture.postCount)
	}
	if got := rootcmd.ExitCodeFromError(runErr); got != rootcmd.ExitUsage {
		t.Fatalf("exit code = %d, want %d (err=%v)", got, rootcmd.ExitUsage, runErr)
	}
	if !strings.Contains(stderr, "processingState PROCESSING") {
		t.Fatalf("expected processing precondition in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, `asc builds wait --build-id "build-1"`) {
		t.Fatalf("expected builds wait remediation in stderr, got %q", stderr)
	}
}

func TestBuildsAddGroupsPreflightBlocksExpiredBuild(t *testing.T) {
	fixture := &addGroupsPreflightFixture{
		t:          t,
		betaGroups: addGroupsInternalGroupPayload,
		build:      `{"data":{"type":"builds","id":"build-1","attributes":{"version":"42","processingState":"VALID","expired":true,"expirationDate":"2026-01-01T00:00:00Z"}}}`,
	}

	_, stderr, runErr := runAddGroupsPreflight(t, fixture,
		"builds", "add-groups", "--build-id", "build-1", "--group", "group-internal")

	if runErr == nil {
		t.Fatalf("expected preflight failure, got nil error")
	}
	if fixture.postCount != 0 {
		t.Fatalf("expected no beta-group POST, got %d", fixture.postCount)
	}
	if got := rootcmd.ExitCodeFromError(runErr); got != rootcmd.ExitUsage {
		t.Fatalf("exit code = %d, want %d (err=%v)", got, rootcmd.ExitUsage, runErr)
	}
	if !strings.Contains(stderr, "has expired") {
		t.Fatalf("expected expiration precondition in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "asc builds list --app") {
		t.Fatalf("expected builds list remediation in stderr, got %q", stderr)
	}
	if fixture.buildBetaDetailReads != 0 {
		t.Fatalf("expected no buildBetaDetail read for an internal-only add, got %d", fixture.buildBetaDetailReads)
	}
}

func TestBuildsAddGroupsPreflightBlocksExternalMissingExportCompliance(t *testing.T) {
	fixture := &addGroupsPreflightFixture{
		t:               t,
		betaGroups:      addGroupsExternalGroupPayload,
		build:           addGroupsValidBuildPayload,
		buildBetaDetail: `{"data":{"type":"buildBetaDetails","id":"detail-1","attributes":{"externalBuildState":"MISSING_EXPORT_COMPLIANCE","internalBuildState":"MISSING_EXPORT_COMPLIANCE"}}}`,
	}

	_, stderr, runErr := runAddGroupsPreflight(t, fixture,
		"builds", "add-groups", "--build-id", "build-1", "--group", "group-external")

	if runErr == nil {
		t.Fatalf("expected preflight failure, got nil error")
	}
	if fixture.postCount != 0 {
		t.Fatalf("expected no beta-group POST, got %d", fixture.postCount)
	}
	if got := rootcmd.ExitCodeFromError(runErr); got != rootcmd.ExitUsage {
		t.Fatalf("exit code = %d, want %d (err=%v)", got, rootcmd.ExitUsage, runErr)
	}
	if !strings.Contains(stderr, "MISSING_EXPORT_COMPLIANCE") {
		t.Fatalf("expected export compliance precondition in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, `asc builds update --build-id "build-1" --uses-non-exempt-encryption=false`) {
		t.Fatalf("expected builds update remediation in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "asc encryption declarations assign-builds") {
		t.Fatalf("expected encryption declaration remediation in stderr, got %q", stderr)
	}
}

func TestBuildsAddGroupsPreflightBlocksExternalBetaRejectedBuild(t *testing.T) {
	fixture := &addGroupsPreflightFixture{
		t:               t,
		betaGroups:      addGroupsExternalGroupPayload,
		build:           addGroupsValidBuildPayload,
		buildBetaDetail: `{"data":{"type":"buildBetaDetails","id":"detail-1","attributes":{"externalBuildState":"BETA_REJECTED"}}}`,
	}

	_, stderr, runErr := runAddGroupsPreflight(t, fixture,
		"builds", "add-groups", "--build-id", "build-1", "--group", "group-external")

	if runErr == nil {
		t.Fatalf("expected preflight failure, got nil error")
	}
	if fixture.postCount != 0 {
		t.Fatalf("expected no beta-group POST, got %d", fixture.postCount)
	}
	if got := rootcmd.ExitCodeFromError(runErr); got != rootcmd.ExitUsage {
		t.Fatalf("exit code = %d, want %d (err=%v)", got, rootcmd.ExitUsage, runErr)
	}
	if !strings.Contains(stderr, "BETA_REJECTED") {
		t.Fatalf("expected beta review precondition in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, `asc testflight review submit --build-id "build-1" --confirm`) {
		t.Fatalf("expected review submit remediation in stderr, got %q", stderr)
	}
}

func TestBuildsAddGroupsPreflightSkipsExternalChecksForInternalOnlyAdd(t *testing.T) {
	fixture := &addGroupsPreflightFixture{
		t:          t,
		betaGroups: addGroupsInternalGroupPayload,
		build:      addGroupsValidBuildPayload,
	}

	stdout, stderr, runErr := runAddGroupsPreflight(t, fixture,
		"builds", "add-groups", "--build-id", "build-1", "--group", "group-internal")

	if runErr != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", runErr, stderr)
	}
	if fixture.buildBetaDetailReads != 0 {
		t.Fatalf("expected no buildBetaDetail read for an internal-only add, got %d", fixture.buildBetaDetailReads)
	}
	if fixture.postCount != 1 {
		t.Fatalf("expected one beta-group POST, got %d", fixture.postCount)
	}
	if !strings.Contains(stdout, `"groupIds":["group-internal"]`) {
		t.Fatalf("expected internal group in output, got %q", stdout)
	}
}

func TestBuildsAddGroupsPreflightAllowsExternalReadyForBetaSubmission(t *testing.T) {
	fixture := &addGroupsPreflightFixture{
		t:               t,
		betaGroups:      addGroupsExternalGroupPayload,
		build:           addGroupsValidBuildPayload,
		buildBetaDetail: `{"data":{"type":"buildBetaDetails","id":"detail-1","attributes":{"externalBuildState":"READY_FOR_BETA_SUBMISSION"}}}`,
	}

	stdout, stderr, runErr := runAddGroupsPreflight(t, fixture,
		"builds", "add-groups", "--build-id", "build-1", "--group", "group-external")

	if runErr != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", runErr, stderr)
	}
	if fixture.postCount != 1 {
		t.Fatalf("expected one beta-group POST, got %d", fixture.postCount)
	}
	if !strings.Contains(stdout, `"groupIds":["group-external"]`) {
		t.Fatalf("expected external group in output, got %q", stdout)
	}
	if !strings.Contains(stderr, "asc testflight review submit") {
		t.Fatalf("expected advisory beta review note in stderr, got %q", stderr)
	}
}

func TestBuildsAddGroupsRendersAppleUnprocessableDetail(t *testing.T) {
	fixture := &addGroupsPreflightFixture{
		t:               t,
		betaGroups:      addGroupsExternalGroupPayload,
		build:           addGroupsValidBuildPayload,
		buildBetaDetail: `{"data":{"type":"buildBetaDetails","id":"detail-1","attributes":{"externalBuildState":"READY_FOR_BETA_TESTING"}}}`,
		postStatus:      http.StatusUnprocessableEntity,
		postBody:        `{"errors":[{"status":"422","code":"STATE_ERROR.ENTITY_STATE_INVALID","title":"The request cannot be fulfilled because of the state of another resource","detail":"Submit this build for beta review before adding external testers."}]}`,
	}

	_, stderr, runErr := runAddGroupsPreflight(t, fixture,
		"builds", "add-groups", "--build-id", "build-1", "--group", "group-external")

	if runErr == nil {
		t.Fatalf("expected Apple 422 failure, got nil error")
	}
	if fixture.postCount != 1 {
		t.Fatalf("expected one beta-group POST, got %d", fixture.postCount)
	}
	wantExit := rootcmd.HTTPStatusToExitCode(http.StatusUnprocessableEntity)
	if got := rootcmd.ExitCodeFromError(runErr); got != wantExit {
		t.Fatalf("exit code = %d, want %d (err=%v)", got, wantExit, runErr)
	}
	if !strings.Contains(stderr, "Submit this build for beta review before adding external testers.") {
		t.Fatalf("expected Apple error detail in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "STATE_ERROR.ENTITY_STATE_INVALID") {
		t.Fatalf("expected Apple error code in stderr, got %q", stderr)
	}
}

func TestBuildsAddGroupsPreflightReadFailureStillAttemptsAdd(t *testing.T) {
	fixture := &addGroupsPreflightFixture{
		t:           t,
		betaGroups:  addGroupsInternalGroupPayload,
		build:       `{"errors":[{"status":"400","code":"BAD_REQUEST","title":"Bad request"}]}`,
		buildStatus: http.StatusBadRequest,
	}

	stdout, stderr, runErr := runAddGroupsPreflight(t, fixture,
		"builds", "add-groups", "--build-id", "build-1", "--group", "group-internal")

	if runErr != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", runErr, stderr)
	}
	if fixture.postCount != 1 {
		t.Fatalf("expected one beta-group POST, got %d", fixture.postCount)
	}
	if !strings.Contains(stdout, `"groupIds":["group-internal"]`) {
		t.Fatalf("expected internal group in output, got %q", stdout)
	}
	if !strings.Contains(stderr, "could not verify build build-1 state") {
		t.Fatalf("expected preflight warning in stderr, got %q", stderr)
	}
}

// serveAddGroupsPreflightState answers the read-only build-state requests the
// beta-group preflight performs. Fixtures that assert on the beta-group
// mutation sequence delegate to it so they stay focused on the mutation.
func serveAddGroupsPreflightState(req *http.Request) (*http.Response, bool, error) {
	const buildID = "build-1"
	if req.Method != http.MethodGet {
		return nil, false, nil
	}
	switch req.URL.Path {
	case "/v1/builds/" + buildID:
		resp, err := jsonResponse(http.StatusOK, `{"data":{"type":"builds","id":"`+buildID+
			`","attributes":{"version":"42","processingState":"VALID","expired":false,"buildAudienceType":"APP_STORE_ELIGIBLE"}}}`)
		return resp, true, err
	case "/v1/builds/" + buildID + "/buildBetaDetail":
		resp, err := jsonResponse(http.StatusOK, `{"data":{"type":"buildBetaDetails","id":"detail-`+buildID+
			`","attributes":{"externalBuildState":"READY_FOR_BETA_TESTING","internalBuildState":"READY_FOR_BETA_TESTING"}}}`)
		return resp, true, err
	}
	return nil, false, nil
}

func TestBuildsAddGroupsSlowPreflightReadStillAttemptsAdd(t *testing.T) {
	restoreBudget := shared.SetBuildBetaGroupPreflightBudgetForTesting(20 * time.Millisecond)
	t.Cleanup(restoreBudget)

	fixture := &addGroupsPreflightFixture{
		t:              t,
		betaGroups:     addGroupsInternalGroupPayload,
		build:          addGroupsValidBuildPayload,
		buildReadDelay: 400 * time.Millisecond,
	}

	stdout, stderr, runErr := runAddGroupsPreflight(t, fixture,
		"builds", "add-groups", "--build-id", "build-1", "--group", "group-internal")

	if runErr != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", runErr, stderr)
	}
	if fixture.postCount != 1 {
		t.Fatalf("expected one beta-group POST after the preflight timed out, got %d", fixture.postCount)
	}
	if !strings.Contains(stdout, `"groupIds":["group-internal"]`) {
		t.Fatalf("expected internal group in output, got %q", stdout)
	}
	if !strings.Contains(stderr, "could not verify build build-1 state") {
		t.Fatalf("expected preflight warning in stderr, got %q", stderr)
	}
}
