package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
	webcore "github.com/rudrankriyam/App-Store-Connect-CLI/internal/web"
)

// writeTestCachedWebSession writes a file-backed cache entry the way the
// session store lays it out, without a last-session pointer.
func writeTestCachedWebSession(t *testing.T, dir, email string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	raw, err := json.Marshal(map[string]any{
		"version":    1,
		"user_email": email,
		"cookies": map[string]any{
			"https://appstoreconnect.apple.com": []map[string]any{{"name": "myacinfo", "value": "cookie-" + email}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session-"+hex.EncodeToString(sum[:])+".json"), raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func stubDefaultAppleIDResolverInputs(t *testing.T, cacheDir string) *bytes.Buffer {
	t.Helper()
	t.Setenv("ASC_WEB_SESSION_CACHE_BACKEND", "file")
	t.Setenv("ASC_WEB_SESSION_CACHE_DIR", cacheDir)
	t.Setenv(webPasswordEnv, "")

	origTryResume := tryResumeSessionFn
	origTryResumeLast := tryResumeLastFn
	origNoticeWriter := sessionDefaultNoticeWriter
	origWarningWriter := sessionCacheWarningWriter
	t.Cleanup(func() {
		tryResumeSessionFn = origTryResume
		tryResumeLastFn = origTryResumeLast
		sessionDefaultNoticeWriter = origNoticeWriter
		sessionCacheWarningWriter = origWarningWriter
	})

	stderr := &bytes.Buffer{}
	sessionDefaultNoticeWriter = stderr
	sessionCacheWarningWriter = stderr
	tryResumeLastFn = func(ctx context.Context) (*webcore.AuthSession, bool, error) {
		return nil, false, nil
	}
	tryResumeSessionFn = func(ctx context.Context, username string) (*webcore.AuthSession, bool, error) {
		t.Fatalf("unexpected user-scoped cache lookup for %q", username)
		return nil, false, nil
	}
	return stderr
}

func TestResolveSessionDefaultsToSoleCachedAppleID(t *testing.T) {
	dir := t.TempDir()
	stderr := stubDefaultAppleIDResolverInputs(t, dir)
	writeTestCachedWebSession(t, dir, "only@example.com")

	var lookups []string
	tryResumeSessionFn = func(ctx context.Context, username string) (*webcore.AuthSession, bool, error) {
		lookups = append(lookups, username)
		return &webcore.AuthSession{UserEmail: username}, true, nil
	}

	session, source, err := resolveSession(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("resolveSession() error = %v", err)
	}
	if source != "cache" {
		t.Fatalf("source = %q, want cache", source)
	}
	if session == nil || session.UserEmail != "only@example.com" {
		t.Fatalf("session = %+v, want only@example.com", session)
	}
	if want := []string{"only@example.com"}; strings.Join(lookups, ",") != strings.Join(want, ",") {
		t.Fatalf("lookups = %v, want %v", lookups, want)
	}
	if got, want := stderr.String(), "Using cached web session for only@example.com; pass --apple-id to override\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestResolveSessionWithoutCachedSessionsPointsToLogin(t *testing.T) {
	dir := t.TempDir()
	stderr := stubDefaultAppleIDResolverInputs(t, dir)

	_, _, err := resolveSession(context.Background(), "", "", "")
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected usage error, got %v", err)
	}
	if got, want := err.Error(), "--apple-id is required when no cached web session is available; run 'asc web auth login --apple-id EMAIL'"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	diagnostic, ok := shared.DiagnosticFromError(err)
	if !ok || diagnostic.Code != shared.DiagnosticRequiredInputMissing || diagnostic.Parameter != "--apple-id" {
		t.Fatalf("diagnostic = %+v (found=%v), want required_input_missing --apple-id", diagnostic, ok)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestResolveSessionWithMultipleCachedSessionsListsAppleIDs(t *testing.T) {
	dir := t.TempDir()
	stderr := stubDefaultAppleIDResolverInputs(t, dir)
	writeTestCachedWebSession(t, dir, "zed@example.com")
	writeTestCachedWebSession(t, dir, "amy@example.com")

	_, _, err := resolveSession(context.Background(), "", "", "")
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected usage error, got %v", err)
	}
	if got, want := err.Error(), "--apple-id is required: multiple cached web sessions are available (amy@example.com, zed@example.com); pass --apple-id to choose one"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	diagnostic, ok := shared.DiagnosticFromError(err)
	if !ok || diagnostic.Code != shared.DiagnosticRequiredInputMissing || diagnostic.Parameter != "--apple-id" {
		t.Fatalf("diagnostic = %+v (found=%v), want required_input_missing --apple-id", diagnostic, ok)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestResolveSessionExplicitAppleIDSkipsCachedDefault(t *testing.T) {
	dir := t.TempDir()
	stderr := stubDefaultAppleIDResolverInputs(t, dir)
	writeTestCachedWebSession(t, dir, "zed@example.com")
	writeTestCachedWebSession(t, dir, "amy@example.com")

	tryResumeLastFn = func(ctx context.Context) (*webcore.AuthSession, bool, error) {
		t.Fatal("did not expect last-session lookup when --apple-id is set")
		return nil, false, nil
	}
	var lookups []string
	tryResumeSessionFn = func(ctx context.Context, username string) (*webcore.AuthSession, bool, error) {
		lookups = append(lookups, username)
		return &webcore.AuthSession{UserEmail: username}, true, nil
	}

	session, _, err := resolveSession(context.Background(), " flag@example.com ", "", "")
	if err != nil {
		t.Fatalf("resolveSession() error = %v", err)
	}
	if session.UserEmail != "flag@example.com" {
		t.Fatalf("session.UserEmail = %q, want flag@example.com", session.UserEmail)
	}
	if strings.Join(lookups, ",") != "flag@example.com" {
		t.Fatalf("lookups = %v, want [flag@example.com]", lookups)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestResolveSessionPrefersLastCachedSessionOverSoleDefault(t *testing.T) {
	dir := t.TempDir()
	stderr := stubDefaultAppleIDResolverInputs(t, dir)
	writeTestCachedWebSession(t, dir, "only@example.com")

	tryResumeLastFn = func(ctx context.Context) (*webcore.AuthSession, bool, error) {
		return &webcore.AuthSession{UserEmail: "last@example.com"}, true, nil
	}

	session, _, err := resolveSession(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("resolveSession() error = %v", err)
	}
	if session.UserEmail != "last@example.com" {
		t.Fatalf("session.UserEmail = %q, want last@example.com", session.UserEmail)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestResolveSessionDefaultLookupFailureFallsBackToUsageError(t *testing.T) {
	dir := t.TempDir()
	stderr := stubDefaultAppleIDResolverInputs(t, dir)

	origDefault := defaultCachedAppleIDFn
	t.Cleanup(func() { defaultCachedAppleIDFn = origDefault })
	defaultCachedAppleIDFn = func() (string, error) {
		return "", errors.New("boom")
	}

	_, _, err := resolveSession(context.Background(), "", "", "")
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected usage error, got %v", err)
	}
	if !strings.Contains(err.Error(), "run 'asc web auth login") {
		t.Fatalf("error = %q, want login hint", err)
	}
	if !strings.Contains(stderr.String(), "Warning: listing cached web sessions failed: boom") {
		t.Fatalf("stderr = %q, want cache listing warning", stderr.String())
	}
}

func TestResolveWebSessionForCommandSessionFromEnvSkipsCachedDefault(t *testing.T) {
	dir := t.TempDir()
	stubDefaultAppleIDResolverInputs(t, dir)
	writeTestCachedWebSession(t, dir, "only@example.com")
	t.Setenv(webSessionBundleEnvName, "")

	origDefault := defaultCachedAppleIDFn
	t.Cleanup(func() { defaultCachedAppleIDFn = origDefault })
	defaultCachedAppleIDFn = func() (string, error) {
		t.Fatal("did not expect cached-session default resolution with --session-from-env")
		return "", nil
	}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	flags := bindWebSessionFlagsWithSessionFromEnv(fs)
	if err := fs.Parse([]string{"--session-from-env"}); err != nil {
		t.Fatal(err)
	}
	_, _, cancel, err := resolveWebSessionForCommand(context.Background(), flags)
	defer cancel()
	if err == nil || !strings.Contains(err.Error(), webSessionBundleEnvName+" is unset or empty") {
		t.Fatalf("error = %v, want unset ASC_WEB_SESSION usage error", err)
	}
}

// TestWrapWebAuthCapabilitiesSessionErrorPreservesAppleIDUsageErrors pins the
// interaction with the capability-specific session diagnostics: both --apple-id
// usage errors are already printed with their own guidance, so the capabilities
// wrapper must pass them through instead of restating them as a generic
// session failure.
func TestWrapWebAuthCapabilitiesSessionErrorPreservesAppleIDUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func() error
	}{
		{name: "no cached session", make: missingAppleIDUsageError},
		{
			name: "ambiguous cache",
			make: func() error {
				return ambiguousAppleIDUsageError([]string{"amy@example.com", "zed@example.com"})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err, wrapped error
			captureOutput(t, func() {
				err = tc.make()
				wrapped = wrapWebAuthCapabilitiesSessionError(err)
			})
			if got, want := wrapped.Error(), err.Error(); got != want {
				t.Fatalf("wrapped error = %q, want the original guidance %q", got, want)
			}
			if !errors.Is(wrapped, flag.ErrHelp) {
				t.Fatalf("wrapped error lost its usage classification: %v", wrapped)
			}
			if got := shared.ClassifyUsageError(wrapped); got != shared.UsageErrorMissingRequired {
				t.Fatalf("usage classification = %q, want %q", got, shared.UsageErrorMissingRequired)
			}
		})
	}
}
