package shared

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/asc"
)

// shellSafeWordPattern matches values that need no quoting in a POSIX shell.
// Tilde, braces, and every metacharacter are deliberately excluded so they
// reach the quoting branches below.
var shellSafeWordPattern = regexp.MustCompile(`^[A-Za-z0-9@%_+=:,./-]+$`)

// ShellQuote renders value as a single POSIX shell word so a printed
// re-invocation can be copied and run without the shell expanding the value.
// Values carrying terminal control characters use bash/zsh ANSI-C quoting,
// which keeps those bytes escaped instead of emitting them to a terminal.
func ShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if shellSafeWordPattern.MatchString(value) {
		return value
	}
	if asc.HasInterpretedTerminalSequence(value) {
		escaped := strconv.Quote(value)
		escaped = escaped[1 : len(escaped)-1]
		// Double quotes are literal inside ANSI-C quoting; single quotes end it.
		escaped = strings.ReplaceAll(escaped, `\"`, `"`)
		escaped = strings.ReplaceAll(escaped, `'`, `\'`)
		return "$'" + escaped + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
