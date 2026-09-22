package signing

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/asc"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
	modernpkcs12 "software.sslmate.com/src/go-pkcs12"
)

func TestSigningFetchCreateMissingCertificateUsage(t *testing.T) {
	cmd := SigningFetchCommand()
	cmd.FlagSet.SetOutput(io.Discard)
	if err := cmd.Parse([]string{
		"--bundle-id", "com.example.app",
		"--profile-type", "IOS_APP_STORE",
		"--create-missing-certificate",
		"--identity-password-file", "password",
	}); err != nil {
		t.Fatal(err)
	}
	err := cmd.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--create-missing-certificate requires --create-missing") {
		t.Fatalf("error = %v", err)
	}
}

func TestSigningFetchCreatesCertificateThenSkipsOnRerun(t *testing.T) {
	t.Setenv("ASC_APP_ID", "")
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
	var posted int
	var certificateContent string
	client := newSigningFetchTestClient(t, func(req *http.Request) *http.Response {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/profiles") && strings.Contains(req.URL.Path, "/certificates"):
			return signingFetchJSONResponse(http.StatusOK, `{"data":[{"type":"certificates","id":"cert-1","attributes":{"displayName":"iOS Distribution","expirationDate":"2099-01-01T00:00:00Z","certificateContent":"`+certificateContent+`"}}]}`)
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/profiles"):
			if posted == 0 {
				return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
			}
			return signingFetchJSONResponse(http.StatusOK, `{"data":[{"type":"profiles","id":"profile-1","attributes":{"name":"App Store","profileType":"IOS_APP_STORE","profileState":"ACTIVE","profileContent":"`+base64.StdEncoding.EncodeToString([]byte("profile"))+`"}}]}`)
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/bundleIds"):
			return signingFetchJSONResponse(http.StatusOK, `{"data":[{"type":"bundleIds","id":"bundle-1","attributes":{"identifier":"com.example.app"}}]}`)
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/certificates"):
			if posted == 0 {
				return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
			}
			return signingFetchJSONResponse(http.StatusOK, `{"data":[{"type":"certificates","id":"cert-1","attributes":{"displayName":"iOS Distribution","expirationDate":"2099-01-01T00:00:00Z","certificateContent":"`+certificateContent+`"}}]}`)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/certificates":
			posted++
			body, _ := io.ReadAll(req.Body)
			var payload struct {
				Data struct {
					Attributes struct {
						CSRContent      string `json:"csrContent"`
						CertificateType string `json:"certificateType"`
					} `json:"attributes"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("decode csr: %v", err)
			}
			if payload.Data.Attributes.CertificateType != "IOS_DISTRIBUTION" {
				t.Fatalf("certificate type = %s", payload.Data.Attributes.CertificateType)
			}
			der, err := base64.StdEncoding.DecodeString(payload.Data.Attributes.CSRContent)
			if err != nil {
				t.Fatal(err)
			}
			csr, err := x509.ParseCertificateRequest(der)
			if err != nil {
				t.Fatal(err)
			}
			certificateContent = issuedCertificateContent(t, csr)
			return signingFetchJSONResponse(http.StatusCreated, `{"data":{"type":"certificates","id":"cert-1","attributes":{"displayName":"iOS Distribution","expirationDate":"2099-01-01T00:00:00Z","certificateContent":"`+certificateContent+`"}}}`)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/profiles":
			return signingFetchJSONResponse(http.StatusCreated, `{"data":{"type":"profiles","id":"profile-1","attributes":{"name":"App Store","profileContent":"`+base64.StdEncoding.EncodeToString([]byte("profile"))+`","profileType":"IOS_APP_STORE"}}}`)
		default:
			t.Fatalf("unexpected %s %s", req.Method, req.URL.Path)
			return nil
		}
	})
	t.Cleanup(shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) {
		return client, nil
	}))
	output := t.TempDir()
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstOutput := filepath.Join(output, "first")
	secondOutput := filepath.Join(output, "second")
	run := func(dir string) (string, error) {
		cmd := SigningFetchCommand()
		cmd.FlagSet.SetOutput(io.Discard)
		if err := cmd.Parse([]string{
			"--bundle-id", "com.example.app",
			"--profile-type", "IOS_APP_STORE",
			"--create-missing",
			"--create-missing-certificate",
			"--identity-password-file", password,
			"--output", dir,
			"--format", "json",
		}); err != nil {
			t.Fatal(err)
		}
		var runErr error
		stdout, _ := captureOutput(t, func() {
			runErr = cmd.Run(context.Background())
		})
		return stdout, runErr
	}
	stdout, err := run(firstOutput)
	if err != nil {
		t.Fatalf("first run: %v\n%s", err, stdout)
	}
	if strings.Contains(stdout, "PRIVATE KEY") {
		t.Fatal("stdout included the private key")
	}
	var result struct {
		CertificateCreated *bool  `json:"certificateCreated"`
		P12Path            string `json:"p12Path"`
		PrivateKeyPath     string `json:"privateKeyPath"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.CertificateCreated == nil || !*result.CertificateCreated || result.P12Path == "" || result.PrivateKeyPath == "" {
		t.Fatalf("result = %#v", result)
	}
	p12, err := os.ReadFile(result.P12Path)
	if err != nil {
		t.Fatalf("read generated p12: %v", err)
	}
	if _, _, err := modernpkcs12.Decode(p12, "secret"); err != nil {
		t.Fatalf("decode generated p12 with normalized password: %v", err)
	}
	if _, _, err := modernpkcs12.Decode(p12, "secret\n"); err == nil {
		t.Fatal("generated p12 retained the password file newline")
	}
	if posted != 1 {
		t.Fatalf("posted %d certificates", posted)
	}
	stdout, err = run(secondOutput)
	if err != nil {
		t.Fatalf("rerun: %v\n%s", err, stdout)
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.CertificateCreated == nil || *result.CertificateCreated {
		t.Fatalf("rerun certificateCreated = %#v", result.CertificateCreated)
	}
	if posted != 1 {
		t.Fatal("rerun posted another certificate")
	}
}

func TestSigningFetchReportsPartialCertificateCreate(t *testing.T) {
	t.Setenv("ASC_APP_ID", "")
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
	client := newSigningFetchTestClient(t, func(req *http.Request) *http.Response {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/bundleIds"):
			return signingFetchJSONResponse(http.StatusOK, `{"data":[{"type":"bundleIds","id":"bundle-1","attributes":{"identifier":"com.example.app"}}]}`)
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/certificates"):
			return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/certificates":
			body, _ := io.ReadAll(req.Body)
			var payload struct {
				Data struct {
					Attributes struct {
						CSRContent string `json:"csrContent"`
					} `json:"attributes"`
				} `json:"data"`
			}
			_ = json.Unmarshal(body, &payload)
			der, _ := base64.StdEncoding.DecodeString(payload.Data.Attributes.CSRContent)
			csr, err := x509.ParseCertificateRequest(der)
			if err != nil {
				t.Fatal(err)
			}
			content := issuedCertificateContent(t, csr)
			return signingFetchJSONResponse(http.StatusCreated, `{"data":{"type":"certificates","id":"cert-1","attributes":{"displayName":"iOS Distribution","expirationDate":"2099-01-01T00:00:00Z","certificateContent":"`+content+`"}}}`)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/profiles":
			return signingFetchJSONResponse(http.StatusInternalServerError, `{"errors":[{"status":"500","code":"UNEXPECTED","detail":"profile create failed"}]}`)
		default:
			t.Fatalf("unexpected %s %s", req.Method, req.URL.Path)
			return nil
		}
	})
	t.Cleanup(shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) { return client, nil }))
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := SigningFetchCommand()
	cmd.FlagSet.SetOutput(io.Discard)
	if err := cmd.Parse([]string{
		"--bundle-id", "com.example.app",
		"--profile-type", "IOS_APP_STORE",
		"--create-missing",
		"--create-missing-certificate",
		"--identity-password-file", password,
		"--output", t.TempDir(),
		"--format", "json",
	}); err != nil {
		t.Fatal(err)
	}
	var runErr error
	stdout, _ := captureOutput(t, func() { runErr = cmd.Run(context.Background()) })
	if runErr == nil {
		t.Fatal("expected profile create failure")
	}
	if !strings.Contains(stdout, `"certificateCreated":true`) {
		t.Fatalf("partial receipt missing certificateCreated: %s", stdout)
	}
}

func TestSigningFetchPreflightsCertificateCreationBeforeCertificatePOST(t *testing.T) {
	t.Setenv("ASC_APP_ID", "")
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
	certificatePosts := 0
	client := newSigningFetchTestClient(t, func(req *http.Request) *http.Response {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/bundleIds"):
			return signingFetchJSONResponse(http.StatusOK, `{"data":[{"type":"bundleIds","id":"bundle-1","attributes":{"identifier":"com.example.app"}}]}`)
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/profiles"):
			return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/certificates":
			return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/certificates":
			certificatePosts++
			return signingFetchJSONResponse(http.StatusInternalServerError, `{"errors":[{"detail":"must not create"}]}`)
		default:
			t.Fatalf("unexpected %s %s", req.Method, req.URL.Path)
			return nil
		}
	})
	t.Cleanup(shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) { return client, nil }))

	output := t.TempDir()
	sentinel := []byte("existing metadata")
	metadataPath := filepath.Join(output, "profiles.json")
	if err := os.WriteFile(metadataPath, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := SigningFetchCommand()
	cmd.FlagSet.SetOutput(io.Discard)
	if err := cmd.Parse([]string{
		"--bundle-id", "com.example.app",
		"--profile-type", "IOS_APP_STORE",
		"--create-missing",
		"--create-missing-certificate",
		"--identity-password-file", password,
		"--output", output,
		"--format", "json",
	}); err != nil {
		t.Fatal(err)
	}
	var runErr error
	stdout, _ := captureOutput(t, func() { runErr = cmd.Run(context.Background()) })
	if runErr == nil || !strings.Contains(runErr.Error(), "output file already exists") {
		t.Fatalf("error = %v, want deterministic metadata preflight failure", runErr)
	}
	if certificatePosts != 0 {
		t.Fatalf("certificate POSTs = %d, want 0", certificatePosts)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want no receipt before remote mutation", stdout)
	}
	got, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(sentinel) {
		t.Fatalf("metadata = %q, want sentinel %q", got, sentinel)
	}
}

func TestSigningFetchPreflightsGeneratedOutputStructureBeforeCertificatePOST(t *testing.T) {
	for _, test := range []struct {
		name      string
		pathArgs  func(string) []string
		wantError string
	}{
		{
			name: "missing p12 parent",
			pathArgs: func(output string) []string {
				return []string{"--p12-out", filepath.Join(output, "missing", "distribution.p12")}
			},
			wantError: "inspect output parent",
		},
		{
			name: "duplicate identity paths",
			pathArgs: func(output string) []string {
				shared := filepath.Join(output, "identity.bin")
				return []string{"--key-out", shared, "--csr-out", shared}
			},
			wantError: "output paths must be distinct",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ASC_APP_ID", "")
			t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
			certificatePosts := 0
			client := newSigningFetchTestClient(t, func(req *http.Request) *http.Response {
				switch {
				case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/bundleIds"):
					return signingFetchJSONResponse(http.StatusOK, `{"data":[{"type":"bundleIds","id":"bundle-1","attributes":{"identifier":"com.example.app"}}]}`)
				case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/profiles"):
					return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
				case req.Method == http.MethodGet && req.URL.Path == "/v1/certificates":
					return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
				case req.Method == http.MethodPost && req.URL.Path == "/v1/certificates":
					certificatePosts++
					return signingFetchJSONResponse(http.StatusInternalServerError, `{"errors":[{"detail":"must not create"}]}`)
				default:
					t.Fatalf("unexpected %s %s", req.Method, req.URL.Path)
					return nil
				}
			})
			t.Cleanup(shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) { return client, nil }))

			output := t.TempDir()
			password := filepath.Join(t.TempDir(), "password")
			if err := os.WriteFile(password, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{
				"--bundle-id", "com.example.app",
				"--profile-type", "IOS_APP_STORE",
				"--create-missing",
				"--create-missing-certificate",
				"--identity-password-file", password,
				"--output", output,
				"--format", "json",
			}
			args = append(args, test.pathArgs(output)...)

			cmd := SigningFetchCommand()
			cmd.FlagSet.SetOutput(io.Discard)
			if err := cmd.Parse(args); err != nil {
				t.Fatal(err)
			}
			var runErr error
			stdout, _ := captureOutput(t, func() { runErr = cmd.Run(context.Background()) })
			if runErr == nil || !strings.Contains(runErr.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", runErr, test.wantError)
			}
			if certificatePosts != 0 {
				t.Fatalf("certificate POSTs = %d, want 0", certificatePosts)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want no receipt before remote mutation", stdout)
			}
		})
	}
}

func TestSigningFetchPreservesUnknownCertificateCreationReceipt(t *testing.T) {
	t.Setenv("ASC_APP_ID", "")
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
	client := newSigningFetchTestClient(t, func(req *http.Request) *http.Response {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/bundleIds"):
			return signingFetchJSONResponse(http.StatusOK, `{"data":[{"type":"bundleIds","id":"bundle-1","attributes":{"identifier":"com.example.app"}}]}`)
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/profiles"):
			return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/certificates":
			return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/certificates":
			return nil
		default:
			t.Fatalf("unexpected %s %s", req.Method, req.URL.Path)
			return nil
		}
	})
	t.Cleanup(shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) { return client, nil }))

	output := t.TempDir()
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := SigningFetchCommand()
	cmd.FlagSet.SetOutput(io.Discard)
	if err := cmd.Parse([]string{
		"--bundle-id", "com.example.app",
		"--profile-type", "IOS_APP_STORE",
		"--create-missing",
		"--create-missing-certificate",
		"--identity-password-file", password,
		"--output", output,
		"--format", "json",
	}); err != nil {
		t.Fatal(err)
	}
	var runErr error
	stdout, _ := captureOutput(t, func() { runErr = cmd.Run(context.Background()) })
	if runErr == nil {
		t.Fatal("expected certificate creation failure")
	}
	var receipt struct {
		Partial                  bool   `json:"partial"`
		CertificateCreationState string `json:"certificateCreationState"`
		PrivateKeyPath           string `json:"privateKeyPath"`
		CSRPath                  string `json:"csrPath"`
	}
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatalf("partial receipt = %q: %v", stdout, err)
	}
	if !receipt.Partial || receipt.CertificateCreationState != "unknown" || receipt.PrivateKeyPath == "" || receipt.CSRPath == "" {
		t.Fatalf("receipt = %#v, want partial unknown certificate state and preserved local paths", receipt)
	}
}

func TestSigningFetchPreservesUnknownReceiptWhenCertificateResponseCannotBeParsed(t *testing.T) {
	t.Setenv("ASC_APP_ID", "")
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
	client := newSigningFetchTestClient(t, func(req *http.Request) *http.Response {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/bundleIds"):
			return signingFetchJSONResponse(http.StatusOK, `{"data":[{"type":"bundleIds","id":"bundle-1","attributes":{"identifier":"com.example.app"}}]}`)
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/profiles"):
			return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/certificates":
			return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/certificates":
			return signingFetchJSONResponse(http.StatusCreated, `{"data":{"type":"certificates","id":"cert-unknown","attributes":{"certificateContent":"!!!"}}}`)
		default:
			t.Fatalf("unexpected %s %s", req.Method, req.URL.Path)
			return nil
		}
	})
	t.Cleanup(shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) { return client, nil }))

	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := SigningFetchCommand()
	cmd.FlagSet.SetOutput(io.Discard)
	if err := cmd.Parse([]string{
		"--bundle-id", "com.example.app",
		"--profile-type", "IOS_APP_STORE",
		"--create-missing",
		"--create-missing-certificate",
		"--identity-password-file", password,
		"--output", t.TempDir(),
		"--format", "json",
	}); err != nil {
		t.Fatal(err)
	}
	var runErr error
	stdout, _ := captureOutput(t, func() { runErr = cmd.Run(context.Background()) })
	if runErr == nil {
		t.Fatal("expected certificate response parsing failure")
	}
	var receipt struct {
		CertificateIDs           []string `json:"certificateIds"`
		CertificateCreationState string   `json:"certificateCreationState"`
		Partial                  bool     `json:"partial"`
		P12Path                  string   `json:"p12Path"`
	}
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatalf("decode partial receipt %q: %v", stdout, err)
	}
	if !receipt.Partial || receipt.CertificateCreationState != "unknown" || strings.Join(receipt.CertificateIDs, ",") != "cert-unknown" || receipt.P12Path != "" {
		t.Fatalf("receipt = %#v, want unknown state with certificate ID and no p12", receipt)
	}
}

func TestSigningSyncPushPreservesPartialCertificateReceipt(t *testing.T) {
	t.Setenv("ASC_APP_ID", "")
	t.Setenv("ASC_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
	remoteURL, _ := newSigningSyncBareRemote(t)
	repoPassword := filepath.Join(t.TempDir(), "repo-password")
	identityPassword := filepath.Join(t.TempDir(), "identity-password")
	if err := os.WriteFile(repoPassword, []byte("repository-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPassword, []byte("identity-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := newSigningFetchTestClient(t, func(req *http.Request) *http.Response {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1/bundleIds":
			return signingFetchJSONResponse(http.StatusOK, `{"data":[{"type":"bundleIds","id":"bundle-1","attributes":{"identifier":"com.example.app"}}]}`)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/bundleIds/bundle-1/profiles":
			return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/certificates":
			return signingFetchJSONResponse(http.StatusOK, `{"data":[]}`)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/certificates":
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read certificate create request: %v", err)
			}
			var payload asc.CertificateCreateRequest
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("decode certificate create request: %v", err)
			}
			csrDER, err := base64.StdEncoding.DecodeString(payload.Data.Attributes.CSRContent)
			if err != nil {
				t.Fatalf("decode CSR: %v", err)
			}
			csr, err := x509.ParseCertificateRequest(csrDER)
			if err != nil {
				t.Fatalf("parse CSR: %v", err)
			}
			certificateContent := issuedCertificateContentWithTeam(t, csr)
			return signingFetchJSONResponse(http.StatusCreated, fmt.Sprintf(`{"data":{"type":"certificates","id":"certificate-new","attributes":{"serialNumber":"new-serial","certificateType":"IOS_DISTRIBUTION","expirationDate":"2100-01-01T00:00:00Z","certificateContent":%q}}}`, certificateContent))
		case req.Method == http.MethodPost && req.URL.Path == "/v1/profiles":
			return signingFetchJSONResponse(http.StatusInternalServerError, `{"errors":[{"status":"500","detail":"profile create failed"}]}`)
		default:
			t.Fatalf("unexpected %s %s", req.Method, req.URL.String())
			return nil
		}
	})
	t.Cleanup(shared.SetASCClientFactoryForTesting(func() (*asc.Client, error) { return client, nil }))

	cmd := syncPushCommand()
	cmd.FlagSet.SetOutput(io.Discard)
	if err := cmd.Parse([]string{
		"--bundle-id", "com.example.app",
		"--profile-type", "IOS_APP_STORE",
		"--repo", remoteURL,
		"--password-file", repoPassword,
		"--create-missing",
		"--create-missing-certificate",
		"--identity-password-file", identityPassword,
		"--output", "json",
	}); err != nil {
		t.Fatal(err)
	}
	var runErr error
	stdout, _ := captureOutput(t, func() { runErr = cmd.Run(context.Background()) })
	if runErr == nil || !strings.Contains(runErr.Error(), "profile create failed") {
		t.Fatalf("error = %v, want profile creation failure", runErr)
	}
	var receipt struct {
		Partial                  bool     `json:"partial"`
		CertificateIDs           []string `json:"certificateIds"`
		CertificateCreationState string   `json:"certificateCreationState"`
		ProfileCreationState     string   `json:"profileCreationState"`
	}
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatalf("partial receipt = %q: %v", stdout, err)
	}
	if !receipt.Partial || receipt.CertificateCreationState != "created" || receipt.ProfileCreationState != "unknown" || strings.Join(receipt.CertificateIDs, ",") != "certificate-new" {
		t.Fatalf("receipt = %#v, want created certificate and unknown profile state", receipt)
	}
}

func issuedCertificateContent(t *testing.T, csr *x509.CertificateRequest) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      csr.Subject,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, csr.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}
