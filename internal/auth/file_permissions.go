package auth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kballard/go-shellquote"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/rootfs"
)

// privateKeyFileMode is the owner-only mode both `auth doctor --fix` and
// `auth login --fix-permissions` apply to a credential file.
const privateKeyFileMode fs.FileMode = 0o600

func filePermissionsTooPermissive(mode fs.FileMode) bool {
	return filePermissionsTooPermissiveForOS(mode, runtime.GOOS)
}

func filePermissionsTooPermissiveForOS(mode fs.FileMode, goos string) bool {
	return goos != "windows" && mode.Perm()&0o077 != 0
}

// FilePermissionRemediationCommand returns the exact command an operator can
// run to tighten an over-permissive credential file. Callers print it so a
// failed read names the one command that repairs the file, and so the
// recommendation text never drifts between `auth doctor` and `auth login`.
//
// The path is quoted for a POSIX shell, so a path holding metacharacters such
// as `$(...)`, a backtick, or `$VAR` is pasted literally instead of being
// expanded, and a leading dash is anchored so chmod reads it as a file rather
// than a flag. A path carrying control characters cannot be displayed as a
// command to copy at all, so it is escaped into an inert Go-quoted form.
func FilePermissionRemediationCommand(path string) string {
	target := path
	if strings.HasPrefix(target, "-") {
		target = "./" + target
	}
	if !utf8.ValidString(target) || containsNonDisplayableRune(target) {
		return fmt.Sprintf("chmod 600 %q", target)
	}
	return shellquote.Join("chmod", "600", target)
}

func containsNonDisplayableRune(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

// FixPrivateKeyFilePermissions tightens an over-permissive private key file to
// 0600 using the same rule ValidateKeyFile and `auth doctor` apply, and reports
// whether the mode changed so callers can tell the operator what was altered.
// A file that is already owner-only is left exactly as it is. The mode is
// applied through the rooted traversal used elsewhere for operator-selected
// paths, so a symlinked component, a directory, or any other non-regular file
// is rejected instead of being changed, and a key that denies its own owner
// read access is still repairable. Failures carry PrivateKeyError kinds,
// keeping the existing private-key diagnostics.
func FixPrivateKeyFilePermissions(path string) (bool, error) {
	return fixPrivateKeyFilePermissionsForOS(path, runtime.GOOS)
}

func fixPrivateKeyFilePermissionsForOS(path, goos string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, newPrivateKeyError(privateKeyAccessErrorKind(err), fmt.Errorf("failed to stat key file: %w", err))
	}
	if info.IsDir() {
		return false, newPrivateKeyError(PrivateKeyInvalidFormat, errors.New("private key path is a directory"))
	}
	if !info.Mode().IsRegular() {
		return false, newPrivateKeyError(PrivateKeyInvalidFormat, errors.New("private key path is not a regular file"))
	}
	if !filePermissionsTooPermissiveForOS(info.Mode(), goos) {
		return false, nil
	}
	if err := rootfs.ChmodFile(path, privateKeyFileMode); err != nil {
		return false, newPrivateKeyError(privateKeyAccessErrorKind(err), fmt.Errorf("failed to change key file permissions: %w", err))
	}
	return true, nil
}
