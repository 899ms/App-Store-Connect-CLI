package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFilePermissionsTooPermissiveForOS(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
		goos string
		want bool
	}{
		{name: "unix secure", mode: 0o600, goos: "darwin", want: false},
		{name: "unix insecure", mode: 0o644, goos: "darwin", want: true},
		{name: "windows secure", mode: 0o600, goos: "windows", want: false},
		{name: "windows insecure", mode: 0o644, goos: "windows", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filePermissionsTooPermissiveForOS(tt.mode, tt.goos); got != tt.want {
				t.Fatalf("filePermissionsTooPermissiveForOS(%#o, %q) = %v, want %v", tt.mode.Perm(), tt.goos, got, tt.want)
			}
		})
	}
}

func TestValidateKeyFileForOSWindowsSkipsUnixPermissionCheck(t *testing.T) {
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "AuthKey.p8")

	writeECDSAPEM(t, keyPath, 0o644, true)

	if err := validateKeyFileForOS(keyPath, "windows"); err != nil {
		t.Fatalf("expected Windows validation to ignore Unix permission bits, got %v", err)
	}
}

func TestFilePermissionRemediationCommand(t *testing.T) {
	command := FilePermissionRemediationCommand("/tmp/keys/AuthKey.p8")
	if command != `chmod 600 "/tmp/keys/AuthKey.p8"` {
		t.Fatalf("FilePermissionRemediationCommand() = %q", command)
	}
	if escaped := FilePermissionRemediationCommand("/tmp/ke\ny.p8"); strings.Contains(escaped, "\n") {
		t.Fatalf("remediation command must escape control characters, got %q", escaped)
	}
}

func TestFixPrivateKeyFilePermissionsTightensPermissiveKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX key permissions")
	}
	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	writeECDSAPEM(t, path, 0o644, true)

	changed, err := FixPrivateKeyFilePermissions(path)
	if err != nil {
		t.Fatalf("FixPrivateKeyFilePermissions() error: %v", err)
	}
	if !changed {
		t.Fatal("expected permissions to be reported as changed")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %#o, want 0600", info.Mode().Perm())
	}
	if err := ValidateKeyFile(path); err != nil {
		t.Fatalf("ValidateKeyFile() after repair error: %v", err)
	}
}

func TestFixPrivateKeyFilePermissionsLeavesOwnerOnlyKeyUntouched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX key permissions")
	}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		path := filepath.Join(t.TempDir(), "AuthKey.p8")
		writeECDSAPEM(t, path, 0o600, true)
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("Chmod() error: %v", err)
		}

		changed, err := FixPrivateKeyFilePermissions(path)
		if err != nil {
			t.Fatalf("FixPrivateKeyFilePermissions() error: %v", err)
		}
		if changed {
			t.Fatalf("mode %#o must not be reported as changed", mode)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error: %v", err)
		}
		if info.Mode().Perm() != mode.Perm() {
			t.Fatalf("permissions = %#o, want %#o preserved", info.Mode().Perm(), mode.Perm())
		}
	}
}

func TestFixPrivateKeyFilePermissionsRejectsUnsupportedFileIdentities(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX key permissions")
	}
	tempDir := t.TempDir()

	if _, err := FixPrivateKeyFilePermissions(filepath.Join(tempDir, "missing.p8")); err == nil {
		t.Fatal("expected missing key file to fail")
	} else {
		assertPrivateKeyErrorKind(t, err, PrivateKeyNotFound)
	}

	if _, err := FixPrivateKeyFilePermissions(tempDir); err == nil {
		t.Fatal("expected directory to fail")
	} else {
		assertPrivateKeyErrorKind(t, err, PrivateKeyInvalidFormat)
	}

	target := filepath.Join(tempDir, "AuthKey.p8")
	writeECDSAPEM(t, target, 0o644, true)
	link := filepath.Join(tempDir, "link.p8")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}
	if _, err := FixPrivateKeyFilePermissions(link); err == nil {
		t.Fatal("expected symlinked key path to fail")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("symlink target permissions = %#o, want 0644 untouched", info.Mode().Perm())
	}
}

func TestFixPrivateKeyFilePermissionsRepairsKeyTheOwnerCannotRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX key permissions")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses owner read permission")
	}
	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	writeECDSAPEM(t, path, 0o600, true)
	if err := os.Chmod(path, 0o044); err != nil {
		t.Fatalf("Chmod() error: %v", err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Fatal("expected the owner to be unable to read the key before repair")
	}

	changed, err := FixPrivateKeyFilePermissions(path)
	if err != nil {
		t.Fatalf("FixPrivateKeyFilePermissions() error: %v", err)
	}
	if !changed {
		t.Fatal("expected permissions to be reported as changed")
	}
	if err := ValidateKeyFile(path); err != nil {
		t.Fatalf("ValidateKeyFile() after repair error: %v", err)
	}
}
