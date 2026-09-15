package cmdtest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// Apple's live 409 on POST /v1/betaGroups/{id}/relationships/betaTesters when
// the membership cannot be created as requested. Quoted verbatim from Apple
// Developer Forums thread 745785, which reports it for a tester the group
// already appears to contain. The code alone is therefore not decisive: the
// same code is returned when the tester genuinely cannot be assigned, so the
// membership read-back decides which case this is.
const betaGroupAddTestersStateConflictBody = `{
  "errors" : [ {
    "id" : "7d7f28bc-60e5-438e-9a29-f17e14b533bc",
    "status" : "409",
    "code" : "STATE_ERROR",
    "title" : "The request cannot be fulfilled because of the state of another resource.",
    "detail" : "Tester(s) cannot be assigned"
  } ]
}`

// The relationship-rejection 409 this repository already records for the
// sibling POST /v1/betaTesters/{id}/relationships/betaGroups conflict (see
// betaTesterGroupConflictAlreadySatisfied in internal/cli/testflight).
const betaGroupAddTestersRelationshipConflictBody = `{"errors":[{"id":"29f5cf4e-6a41-4d03-9c4f-4c8d2b6a1d55","status":"409","code":"ENTITY_ERROR.RELATIONSHIP.INVALID","title":"The provided entity includes a relationship with an invalid value","detail":"The relationship 'betaTesters' includes a value that is already related to this resource."}]}`

type addTestersConflictRequest struct {
	method string
	path   string
}

// stubAddTestersTransport replays a POST conflict followed by membership
// read-backs. membership maps a tester ID to the group IDs its
// relationships/betaGroups read reports.
func stubAddTestersTransport(
	t *testing.T,
	postStatus int,
	postBody string,
	membership map[string][]string,
	readBackStatus int,
) *[]addTestersConflictRequest {
	t.Helper()

	originalTransport := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})

	requests := make([]addTestersConflictRequest, 0, 4)
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, addTestersConflictRequest{method: req.Method, path: req.URL.Path})

		if req.Method == http.MethodPost {
			if req.URL.Path != "/v1/betaGroups/group-1/relationships/betaTesters" {
				t.Fatalf("unexpected POST path %q", req.URL.Path)
			}
			return jsonResponse(postStatus, postBody)
		}

		if req.Method != http.MethodGet {
			t.Fatalf("unexpected %s request to %q", req.Method, req.URL.Path)
		}
		if readBackStatus != 0 && readBackStatus != http.StatusOK {
			return jsonResponse(readBackStatus, `{"errors":[{"status":"500","code":"UNEXPECTED_ERROR","title":"An unexpected error occurred.","detail":"Request failed."}]}`)
		}

		prefix := "/v1/betaTesters/"
		suffix := "/relationships/betaGroups"
		if !strings.HasPrefix(req.URL.Path, prefix) || !strings.HasSuffix(req.URL.Path, suffix) {
			t.Fatalf("unexpected read-back path %q", req.URL.Path)
		}
		testerID := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, prefix), suffix)

		groups := membership[testerID]
		linkages := make([]string, 0, len(groups))
		for _, groupID := range groups {
			linkages = append(linkages, `{"type":"betaGroups","id":"`+groupID+`"}`)
		}
		return jsonResponse(http.StatusOK, `{"data":[`+strings.Join(linkages, ",")+`],"links":{}}`)
	})

	return &requests
}

type betaGroupAddTestersReceipt struct {
	GroupID        string   `json:"groupId"`
	TesterIDs      []string `json:"testerIds"`
	Action         string   `json:"action"`
	AlreadyPresent bool     `json:"alreadyPresent"`
}

func runAddTesters(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

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

func TestBetaGroupsAddTestersAlreadyMemberIsExpectedNegative(t *testing.T) {
	for _, conflict := range []struct {
		name string
		body string
	}{
		{name: "state error", body: betaGroupAddTestersStateConflictBody},
		{name: "relationship invalid", body: betaGroupAddTestersRelationshipConflictBody},
	} {
		t.Run(conflict.name, func(t *testing.T) {
			setupAuth(t)
			t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "nonexistent.json"))

			requests := stubAddTestersTransport(t, http.StatusConflict, conflict.body, map[string][]string{
				"tester-1": {"group-0", "group-1"},
			}, http.StatusOK)

			stdout, stderr, err := runAddTesters(
				t,
				"testflight", "groups", "add-testers",
				"--group", "group-1",
				"--tester", "tester-1",
				"--output", "json",
			)
			if err != nil {
				t.Fatalf("expected an already-present tester to succeed, got %v", err)
			}

			var receipt betaGroupAddTestersReceipt
			if jsonErr := json.Unmarshal([]byte(stdout), &receipt); jsonErr != nil {
				t.Fatalf("parse stdout JSON: %v; stdout=%q", jsonErr, stdout)
			}
			if receipt.GroupID != "group-1" {
				t.Fatalf("receipt groupId = %q, want group-1", receipt.GroupID)
			}
			if len(receipt.TesterIDs) != 1 || receipt.TesterIDs[0] != "tester-1" {
				t.Fatalf("receipt testerIds = %v, want [tester-1]", receipt.TesterIDs)
			}
			if receipt.Action != "skipped" {
				t.Fatalf("receipt action = %q, want skipped", receipt.Action)
			}
			if !receipt.AlreadyPresent {
				t.Fatalf("receipt alreadyPresent = false, want true; stdout=%q", stdout)
			}
			if !strings.Contains(stderr, "already in group group-1") {
				t.Fatalf("expected stderr to report the already-present tester, got %q", stderr)
			}
			if got := *requests; len(got) != 2 || got[0].method != http.MethodPost || got[1].method != http.MethodGet {
				t.Fatalf("expected one POST then one membership read-back, got %v", got)
			}
		})
	}
}

func TestBetaGroupsAddTestersConflictWithoutMembershipStillFails(t *testing.T) {
	setupAuth(t)
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "nonexistent.json"))

	requests := stubAddTestersTransport(t, http.StatusConflict, betaGroupAddTestersStateConflictBody, map[string][]string{
		"tester-1": {"group-0"},
	}, http.StatusOK)

	stdout, _, err := runAddTesters(
		t,
		"testflight", "groups", "add-testers",
		"--group", "group-1",
		"--tester", "tester-1",
		"--output", "json",
	)
	if err == nil {
		t.Fatal("expected a conflict whose read-back finds no membership to fail")
	}
	if !strings.Contains(err.Error(), "Tester(s) cannot be assigned") {
		t.Fatalf("expected Apple's conflict detail to survive, got %v", err)
	}
	if !strings.Contains(err.Error(), "not in group group-1: tester-1") {
		t.Fatalf("expected the read-back result in the diagnostic, got %v", err)
	}
	if stdout != "" {
		t.Fatalf("expected no receipt for a failed add, got %q", stdout)
	}
	if got := *requests; len(got) != 2 {
		t.Fatalf("expected one POST and one read-back, got %v", got)
	}
}

func TestBetaGroupsAddTestersPartialMembershipStillFails(t *testing.T) {
	setupAuth(t)
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "nonexistent.json"))

	stubAddTestersTransport(t, http.StatusConflict, betaGroupAddTestersStateConflictBody, map[string][]string{
		"tester-1": {"group-1"},
		"tester-2": {},
	}, http.StatusOK)

	_, _, err := runAddTesters(
		t,
		"testflight", "groups", "add-testers",
		"--group", "group-1",
		"--tester", "tester-1,tester-2",
		"--output", "json",
	)
	if err == nil {
		t.Fatal("expected a partially satisfied conflict to fail")
	}
	if !strings.Contains(err.Error(), "already in group group-1: tester-1") ||
		!strings.Contains(err.Error(), "not in group group-1: tester-2") {
		t.Fatalf("expected the diagnostic to name both sides, got %v", err)
	}
}

func TestBetaGroupsAddTestersNonConflictFailureSkipsReadBack(t *testing.T) {
	setupAuth(t)
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "nonexistent.json"))

	requests := stubAddTestersTransport(t, http.StatusForbidden,
		`{"errors":[{"status":"403","code":"FORBIDDEN_ERROR","title":"This request is forbidden for security reasons","detail":"The API key in use does not allow this request."}]}`,
		map[string][]string{"tester-1": {"group-1"}}, http.StatusOK)

	_, _, err := runAddTesters(
		t,
		"testflight", "groups", "add-testers",
		"--group", "group-1",
		"--tester", "tester-1",
	)
	if err == nil {
		t.Fatal("expected a non-conflict failure to fail")
	}
	if got := *requests; len(got) != 1 || got[0].method != http.MethodPost {
		t.Fatalf("expected only the POST for a non-conflict failure, got %v", got)
	}
}

func TestBetaGroupsAddTestersReadBackFailureReportsBothErrors(t *testing.T) {
	setupAuth(t)
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "nonexistent.json"))

	stubAddTestersTransport(t, http.StatusConflict, betaGroupAddTestersStateConflictBody,
		map[string][]string{"tester-1": {"group-1"}}, http.StatusInternalServerError)

	_, _, err := runAddTesters(
		t,
		"testflight", "groups", "add-testers",
		"--group", "group-1",
		"--tester", "tester-1",
	)
	if err == nil {
		t.Fatal("expected a failed read-back to keep the add failing")
	}
	if !strings.Contains(err.Error(), "Tester(s) cannot be assigned") {
		t.Fatalf("expected the original conflict in the error, got %v", err)
	}
	if !strings.Contains(err.Error(), "read-back") {
		t.Fatalf("expected the read-back failure in the error, got %v", err)
	}
}

func TestBetaGroupsAddTestersSuccessPrintsCreatedReceipt(t *testing.T) {
	setupAuth(t)
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "nonexistent.json"))

	requests := stubAddTestersTransport(t, http.StatusNoContent, "", nil, http.StatusOK)

	stdout, stderr, err := runAddTesters(
		t,
		"testflight", "groups", "add-testers",
		"--group", "group-1",
		"--tester", "tester-1,tester-2",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("expected a successful add, got %v", err)
	}

	var receipt betaGroupAddTestersReceipt
	if jsonErr := json.Unmarshal([]byte(stdout), &receipt); jsonErr != nil {
		t.Fatalf("parse stdout JSON: %v; stdout=%q", jsonErr, stdout)
	}
	if receipt.Action != "added" {
		t.Fatalf("receipt action = %q, want added", receipt.Action)
	}
	if receipt.AlreadyPresent {
		t.Fatalf("receipt alreadyPresent = true, want false; stdout=%q", stdout)
	}
	if len(receipt.TesterIDs) != 2 {
		t.Fatalf("receipt testerIds = %v, want both requested testers", receipt.TesterIDs)
	}
	if !strings.Contains(stderr, "Successfully added 2 tester(s) to group group-1") {
		t.Fatalf("expected the success diagnostic, got %q", stderr)
	}
	if got := *requests; len(got) != 1 || got[0].method != http.MethodPost {
		t.Fatalf("expected only the POST on the success path, got %v", got)
	}
}
