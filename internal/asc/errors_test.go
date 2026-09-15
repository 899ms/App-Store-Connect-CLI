package asc

import (
	"errors"
	"strings"
	"testing"
)

func TestAPIErrorError_SanitizesControlCharacters(t *testing.T) {
	err := &APIError{
		Title:  "Bad\x1b[31m",
		Detail: "Detail\x07",
		Code:   "CODE\x1b",
		AssociatedErrors: map[string][]APIAssociatedError{
			"/v1/resource\x1b[33m": {
				{
					Code:   "ENTITY_ERROR\x1b",
					Detail: "Associated detail\x07",
				},
			},
		},
	}

	message := err.Error()
	if strings.ContainsAny(message, "\x1b\x07") {
		t.Fatalf("expected control characters to be stripped, got %q", message)
	}
	if !strings.Contains(message, "Bad") || !strings.Contains(message, "Detail") {
		t.Fatalf("expected title and detail in message, got %q", message)
	}
	if !strings.Contains(message, "Associated detail") {
		t.Fatalf("expected associated detail in message, got %q", message)
	}
}

func TestAPIErrorIs_UnauthorizedByStatusCode(t *testing.T) {
	// Apple returns 401 responses with code NOT_AUTHORIZED, not UNAUTHORIZED.
	payload := `{"errors":[{"id":"7091e344-4b31-4b6b-9f04-16d61a1c8c9e","status":"401","code":"NOT_AUTHORIZED","title":"Authentication credentials are missing or invalid.","detail":"Provide a properly configured and signed bearer token, and make sure that it has not expired."}]}`
	err := ParseErrorWithStatus([]byte(payload), 401)

	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected 401 NOT_AUTHORIZED response to match ErrUnauthorized, got %v", err)
	}
	if errors.Is(err, ErrForbidden) {
		t.Fatalf("expected 401 response not to match ErrForbidden, got %v", err)
	}
}

func TestAPIErrorIs_ForbiddenByStatusCode(t *testing.T) {
	payload := `{"errors":[{"status":"403","code":"FORBIDDEN_ERROR","title":"The given operation is not allowed","detail":"This request is forbidden for security reasons"}]}`
	err := ParseErrorWithStatus([]byte(payload), 403)

	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected 403 response to match ErrForbidden, got %v", err)
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected 403 response not to match ErrUnauthorized, got %v", err)
	}
}

func TestAPIErrorIs_AuthCodesWithoutStatusCode(t *testing.T) {
	// Code-string matching must keep working when no HTTP status is known.
	if !errors.Is(&APIError{Code: "UNAUTHORIZED"}, ErrUnauthorized) {
		t.Fatal("expected UNAUTHORIZED code to match ErrUnauthorized")
	}
	if !errors.Is(&APIError{Code: "FORBIDDEN"}, ErrForbidden) {
		t.Fatal("expected FORBIDDEN code to match ErrForbidden")
	}
}

func TestAPIErrorError_AssociatedErrorsSortedByResourcePath(t *testing.T) {
	err := &APIError{
		Title:  "Cannot submit",
		Detail: "Fix associated errors",
		AssociatedErrors: map[string][]APIAssociatedError{
			"/v1/b": {
				{Detail: "B detail"},
			},
			"/v1/a": {
				{Detail: "A detail"},
			},
		},
	}

	message := err.Error()
	aIndex := strings.Index(message, "Associated errors for /v1/a:")
	bIndex := strings.Index(message, "Associated errors for /v1/b:")
	if aIndex == -1 || bIndex == -1 {
		t.Fatalf("expected associated error sections, got %q", message)
	}
	if aIndex > bIndex {
		t.Fatalf("expected associated errors to be sorted by path, got %q", message)
	}
}

func TestParseErrorWithStatus_RetainsEveryErrorCode(t *testing.T) {
	// Apple's live 409 for a duplicate versionString on
	// POST /v1/appStoreVersions carries two errors, and the duplicate code is
	// the second one. Captured against app 6759231657 on 2026-09-15.
	payload := `{"errors":[{"id":"b068c5c0-b3fa-4d12-aa89-f1b9aa061b28","status":"409","code":"ENTITY_ERROR.RELATIONSHIP.INVALID","title":"The provided entity includes a relationship with an invalid value","detail":"You cannot create a new version of the App in the current state.","source":{"pointer":"/data/relationships/app"}},{"id":"eb1884a7-e427-42db-ac95-26c49c84a5c2","status":"409","code":"ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE","title":"The provided entity includes an attribute with a value that has already been used","detail":"The version number has been previously used.","source":{"pointer":"/data/attributes/versionString"}}]}`

	err := ParseErrorWithStatus([]byte(payload), 409)

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("ParseErrorWithStatus returned %T, want *APIError", err)
	}
	if apiErr.Code != "ENTITY_ERROR.RELATIONSHIP.INVALID" {
		t.Fatalf("Code = %q, want the first error's code", apiErr.Code)
	}
	want := []string{"ENTITY_ERROR.RELATIONSHIP.INVALID", "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE"}
	if len(apiErr.AllCodes) != len(want) {
		t.Fatalf("AllCodes = %v, want %v", apiErr.AllCodes, want)
	}
	for i, code := range want {
		if apiErr.AllCodes[i] != code {
			t.Fatalf("AllCodes = %v, want %v", apiErr.AllCodes, want)
		}
	}
}
