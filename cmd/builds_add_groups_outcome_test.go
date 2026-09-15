package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/asc"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/telemetry"
)

// addGroupsOutcomeServer serves the reads that precede the beta-group POST so
// the telemetry classification of each failure mode can be asserted.
func addGroupsOutcomeServer(t *testing.T, buildPayload, betaDetailPayload string, postStatus int, postBody string, postCount *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1/builds/build-1/app":
			fmt.Fprint(w, `{"data":{"type":"apps","id":"app-1"}}`)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/apps/app-1/betaGroups":
			fmt.Fprint(w, `{"data":[{"type":"betaGroups","id":"group-external","attributes":{"name":"External QA","isInternalGroup":false}}]}`)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/builds/build-1":
			fmt.Fprint(w, buildPayload)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/builds/build-1/buildBetaDetail":
			fmt.Fprint(w, betaDetailPayload)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/builds/build-1/relationships/betaGroups":
			*postCount++
			w.WriteHeader(postStatus)
			fmt.Fprint(w, postBody)
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	}))
}

func TestRunBuildsAddGroupsProcessingPreflightIsUsageOutcome(t *testing.T) {
	resetReportFlags(t)
	t.Setenv("ASC_APP_ID", "")

	postCount := 0
	server := addGroupsOutcomeServer(
		t,
		`{"data":{"type":"builds","id":"build-1","attributes":{"version":"42","processingState":"PROCESSING","expired":false}}}`,
		`{"data":{"type":"buildBetaDetails","id":"detail-1","attributes":{"externalBuildState":"PROCESSING"}}}`,
		http.StatusNoContent,
		"",
		&postCount,
	)
	defer server.Close()

	client := newHTTPStatusTestClient(t, server.URL)
	t.Cleanup(shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) {
		return client, nil
	}))

	originalEmitTelemetry := emitTelemetry
	t.Cleanup(func() { emitTelemetry = originalEmitTelemetry })
	var gotExitCode int
	var gotContext telemetry.EventContext
	emitTelemetry = func(_ string, _ string, _ time.Duration, exitCode int, eventContext telemetry.EventContext) {
		gotExitCode = exitCode
		gotContext = eventContext
	}

	_, stderr := captureCommandOutput(t, func() {
		if code := Run([]string{
			"builds", "add-groups",
			"--build-id", "build-1",
			"--group", "group-external",
		}, "4.0.0"); code != ExitUsage {
			t.Fatalf("Run() exit code = %d, want %d (stderr pending)", code, ExitUsage)
		}
	})

	if postCount != 0 {
		t.Fatalf("expected no beta-group POST, got %d", postCount)
	}
	if !strings.Contains(stderr, "processingState PROCESSING") {
		t.Fatalf("stderr = %q, want processing precondition", stderr)
	}
	if gotExitCode != ExitUsage || gotContext.OutcomeKind != telemetry.OutcomeUsageError {
		t.Fatalf("unexpected telemetry: exit=%d context=%+v", gotExitCode, gotContext)
	}
	if gotContext.DiagnosticCode != string(shared.DiagnosticStateNotReady) {
		t.Fatalf("diagnostic code = %q, want %q", gotContext.DiagnosticCode, shared.DiagnosticStateNotReady)
	}
}

func TestRunBuildsAddGroupsUnprocessableIsExpectedNegativeOutcome(t *testing.T) {
	resetReportFlags(t)
	t.Setenv("ASC_APP_ID", "")

	postCount := 0
	server := addGroupsOutcomeServer(
		t,
		`{"data":{"type":"builds","id":"build-1","attributes":{"version":"42","processingState":"VALID","expired":false,"buildAudienceType":"APP_STORE_ELIGIBLE"}}}`,
		`{"data":{"type":"buildBetaDetails","id":"detail-1","attributes":{"externalBuildState":"READY_FOR_BETA_TESTING"}}}`,
		http.StatusUnprocessableEntity,
		`{"errors":[{"status":"422","code":"STATE_ERROR.ENTITY_STATE_INVALID","title":"The request cannot be fulfilled","detail":"This build cannot be added to the requested beta group."}]}`,
		&postCount,
	)
	defer server.Close()

	client := newHTTPStatusTestClient(t, server.URL)
	t.Cleanup(shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) {
		return client, nil
	}))

	originalEmitTelemetry := emitTelemetry
	t.Cleanup(func() { emitTelemetry = originalEmitTelemetry })
	var gotExitCode int
	var gotContext telemetry.EventContext
	emitTelemetry = func(_ string, _ string, _ time.Duration, exitCode int, eventContext telemetry.EventContext) {
		gotExitCode = exitCode
		gotContext = eventContext
	}

	wantExit := HTTPStatusToExitCode(http.StatusUnprocessableEntity)
	_, stderr := captureCommandOutput(t, func() {
		if code := Run([]string{
			"builds", "add-groups",
			"--build-id", "build-1",
			"--group", "group-external",
		}, "4.0.0"); code != wantExit {
			t.Fatalf("Run() exit code = %d, want %d", code, wantExit)
		}
	})

	if postCount != 1 {
		t.Fatalf("expected one beta-group POST, got %d", postCount)
	}
	if !strings.Contains(stderr, "This build cannot be added to the requested beta group.") {
		t.Fatalf("stderr = %q, want Apple error detail", stderr)
	}
	if gotExitCode != wantExit || gotContext.OutcomeKind != telemetry.OutcomeExpectedNegative {
		t.Fatalf("unexpected telemetry: exit=%d context=%+v", gotExitCode, gotContext)
	}
	if gotContext.HTTPStatus != http.StatusUnprocessableEntity {
		t.Fatalf("http status = %d, want 422", gotContext.HTTPStatus)
	}
}
