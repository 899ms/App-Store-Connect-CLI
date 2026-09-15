package auth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"

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
func FilePermissionRemediationCommand(path string) string {
	return fmt.Sprintf("chmod 600 %q", path)
}

// FixPrivateKeyFilePermissions tightens an over-permissive private key file to
// 0600 using the same rule ValidateKeyFile and `auth doctor` apply, and reports
// whether the mode changed so callers can tell the operator what was altered.
// A file that is already owner-only is left exactly as it is. The file is
// opened through the rooted traversal used elsewhere for operator-selected
// paths, so a symlinked component, a directory, or any other non-regular file
// is rejected instead of chmod'ed through. Failures carry PrivateKeyError
// kinds, keeping the existing private-key diagnostics.
func FixPrivateKeyFilePermissions(path string) (bool, error) {
	return fixPrivateKeyFilePermissionsForOS(path, runtime.GOOS)
}

func fixPrivateKeyFilePermissionsForOS(path, goos string) (bool, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return false, newPrivateKeyError(privateKeyAccessErrorKind(err), fmt.Errorf("failed to stat key file: %w", err))
	}
	if pathInfo.IsDir() {
		return false, newPrivateKeyError(PrivateKeyInvalidFormat, errors.New("private key path is a directory"))
	}
	if !pathInfo.Mode().IsRegular() {
		return false, newPrivateKeyError(PrivateKeyInvalidFormat, errors.New("private key path is not a regular file"))
	}

	file, err := rootfs.OpenFile(path)
	if err != nil {
		return false, newPrivateKeyError(privateKeyAccessErrorKind(err), fmt.Errorf("failed to open key file: %w", err))
	}
	defer func() {
		_ = file.Close()
	}()

	info, err := file.Stat()
	if err != nil {
		return false, newPrivateKeyError(privateKeyAccessErrorKind(err), fmt.Errorf("failed to stat key file: %w", err))
	}
	if !info.Mode().IsRegular() {
		return false, newPrivateKeyError(PrivateKeyInvalidFormat, errors.New("private key path is not a regular file"))
	}
	if !os.SameFile(pathInfo, info) {
		return false, newPrivateKeyError(PrivateKeyAccessFailed, errors.New("private key file changed while it was being opened"))
	}
	if !filePermissionsTooPermissiveForOS(info.Mode(), goos) {
		return false, nil
	}
	if err := file.Chmod(privateKeyFileMode); err != nil {
		return false, newPrivateKeyError(privateKeyAccessErrorKind(err), fmt.Errorf("failed to change key file permissions: %w", err))
	}
	return true, nil
}
