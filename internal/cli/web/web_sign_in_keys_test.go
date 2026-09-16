package web

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/rootfs"
	webcore "github.com/rudrankriyam/App-Store-Connect-CLI/internal/web"
)

func TestWebSignInKeysCreateRequiresBundleID(t *testing.T) {
	command := WebSignInKeysCreateCommand()
	if err := command.FlagSet.Parse([]string{"--name", "Sway", "--output-dir", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := captureWebCommandOutput(t, func() {
		if err := command.Exec(context.Background(), nil); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("expected usage error, got %v", err)
		}
	})
	if stdout != "" || !strings.Contains(stderr, "--bundle-id is required") {
		t.Fatalf("unexpected output: %q %q", stdout, stderr)
	}
}

func TestWebSignInKeysCreateSavesPrivateFileAndReceipt(t *testing.T) {
	testWebSignInKeysCreateSavesPrivateFileAndReceipt(t, t.TempDir())
}

func testWebSignInKeysCreateSavesPrivateFileAndReceipt(t *testing.T, directory string) {
	originalResolve, originalClient, originalPersist := resolveSessionFn, newWebClientFn, persistWebSessionFn
	originalCreate, originalDownload := createDeveloperSignInKeyFn, downloadDeveloperSignInKeyFn
	t.Cleanup(func() {
		resolveSessionFn = originalResolve
		newWebClientFn = originalClient
		persistWebSessionFn = originalPersist
		createDeveloperSignInKeyFn = originalCreate
		downloadDeveloperSignInKeyFn = originalDownload
	})
	resolveSessionFn = func(context.Context, string, string, string) (*webcore.AuthSession, string, error) {
		return &webcore.AuthSession{}, "cache", nil
	}
	newWebClientFn = func(*webcore.AuthSession) *webcore.Client { return &webcore.Client{} }
	persistWebSessionFn = func(*webcore.AuthSession) error { return nil }
	createCalls := 0
	createDeveloperSignInKeyFn = func(_ context.Context, _ *webcore.Client, name, bundleID string) (*webcore.DeveloperSignInKey, error) {
		createCalls++
		if name != "Sway" || bundleID != "BUNDLE123" {
			t.Fatal("wrong create request")
		}
		return &webcore.DeveloperSignInKey{KeyID: "KEY123"}, nil
	}
	calls := 0
	downloadDeveloperSignInKeyFn = func(context.Context, *webcore.Client, string) ([]byte, error) {
		calls++
		return []byte("PRIVATE-TEST-MATERIAL"), nil
	}
	command := WebSignInKeysCreateCommand()
	if err := command.FlagSet.Parse([]string{"--name", "Sway", "--bundle-id", "BUNDLE123", "--output-dir", directory, "--output", "json"}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := captureWebCommandOutput(t, func() {
		err := command.Exec(context.Background(), nil)
		if runtime.GOOS == "windows" {
			if !errors.Is(err, rootfs.ErrFileIdentityMutationUnsupported) {
				t.Fatalf("expected unsupported private publication: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	})
	if runtime.GOOS == "windows" {
		if calls != 0 || createCalls != 0 {
			t.Fatal("created or consumed a key on unsupported platform")
		}
		return
	}
	if strings.Contains(stdout+stderr, "PRIVATE-TEST-MATERIAL") {
		t.Fatal("private key leaked")
	}
	var receipt struct {
		KeyID  string `json:"keyId"`
		P8Path string `json:"p8Path"`
	}
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.KeyID != "KEY123" || receipt.P8Path != filepath.Join(directory, "AuthKey_KEY123.p8") {
		t.Fatalf("wrong receipt %+v", receipt)
	}
	info, err := os.Stat(receipt.P8Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("private permissions not preserved")
	}
	saved, err := os.ReadFile(receipt.P8Path)
	if err != nil || string(saved) != "PRIVATE-TEST-MATERIAL" {
		t.Fatal("key not saved")
	}
	download := WebSignInKeysDownloadCommand()
	if err := download.FlagSet.Parse([]string{"--key-id", "KEY123", "--output-dir", directory}); err != nil {
		t.Fatal(err)
	}
	if err := download.Exec(context.Background(), nil); err == nil {
		t.Fatal("overwrote private key")
	}
	if calls != 1 {
		t.Fatal("consumed download despite existing destination")
	}
	createCollision := WebSignInKeysCreateCommand()
	if err := createCollision.FlagSet.Parse([]string{"--name", "Sway", "--bundle-id", "BUNDLE123", "--output-dir", directory}); err != nil {
		t.Fatal(err)
	}
	if err := createCollision.Exec(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "P8 was not downloaded") || !strings.Contains(err.Error(), "--key-id KEY123 --output-dir OTHER_DIR") {
		t.Fatalf("missing safe recovery instructions: %v", err)
	}
	if calls != 1 {
		t.Fatal("consumed key after create destination collision")
	}
	unchanged, err := os.ReadFile(receipt.P8Path)
	if err != nil || string(unchanged) != "PRIVATE-TEST-MATERIAL" {
		t.Fatal("changed existing key after create collision")
	}
	for _, downloadFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("destination replaced, download fails=%t", downloadFails), func(t *testing.T) {
			racedDir := t.TempDir()
			destination := filepath.Join(racedDir, "AuthKey_KEY123.p8")
			downloadDeveloperSignInKeyFn = func(context.Context, *webcore.Client, string) ([]byte, error) {
				if err := os.Remove(destination); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(destination, []byte("OTHER-FILE"), 0o600); err != nil {
					t.Fatal(err)
				}
				if downloadFails {
					return nil, errors.New("download failed")
				}
				return []byte("PRIVATE-TEST-MATERIAL"), nil
			}
			command := WebSignInKeysDownloadCommand()
			if err := command.FlagSet.Parse([]string{"--key-id", "KEY123", "--output-dir", racedDir}); err != nil {
				t.Fatal(err)
			}
			if err := command.Exec(context.Background(), nil); err == nil {
				t.Fatal("expected destination-change error")
			}
			remaining, err := os.ReadFile(destination)
			if err != nil || string(remaining) != "OTHER-FILE" {
				t.Fatal("replaced destination was overwritten or removed")
			}
			if !downloadFails {
				copies, err := filepath.Glob(filepath.Join(racedDir, ".asc-api-key-*.p8"))
				if err != nil || len(copies) != 1 {
					t.Fatalf("expected one recovery copy: %v %v", copies, err)
				}
				material, err := os.ReadFile(copies[0])
				if err != nil || string(material) != "PRIVATE-TEST-MATERIAL" {
					t.Fatal("lost one-time private key")
				}
			}
		})
	}
}

func TestWebSignInKeysDownloadRequiresKeyID(t *testing.T) {
	command := WebSignInKeysDownloadCommand()
	if err := command.FlagSet.Parse([]string{"--output-dir", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	_, stderr := captureWebCommandOutput(t, func() {
		if err := command.Exec(context.Background(), nil); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("expected usage error, got %v", err)
		}
	})
	if !strings.Contains(stderr, "--key-id is required") {
		t.Fatalf("missing error: %q", stderr)
	}
}
