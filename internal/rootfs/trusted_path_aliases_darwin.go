//go:build darwin

package rootfs

// trustedPathAliases returns system-owned lexical directory aliases that may
// safely act as trusted roots. Darwin exposes /tmp as a symlink to
// /private/tmp; selecting the alias itself lets New pin its physical directory
// identity while rooted traversal continues to reject symlinks below it.
func trustedPathAliases() []string {
	return []string{"/tmp"}
}
