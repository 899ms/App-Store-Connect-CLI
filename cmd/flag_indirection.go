package cmd

import (
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
)

// indirectionExcludedFlags lists the flags that never resolve @env:/@file:
// values. They select how asc itself runs (output rendering, credential
// profile, CI report plumbing) and the parse-failure paths read them from the
// raw argv, so resolving them would create two sources of truth.
var indirectionExcludedFlags = map[string]struct{}{
	"output":      {},
	"profile":     {},
	"report":      {},
	"report-file": {},
}

// resolveFlagValueIndirection rewrites argv so that every value-taking flag on
// the resolved command chain receives its @env:NAME or @file:PATH value before
// flag parsing. The result has the same token count and structure as args:
// only value tokens change, so structural analyses of the original args stay
// valid and resolved values never flow into diagnostics.
//
// Boolean flags, excluded flags, positional arguments, and everything after
// `--` are untouched. Rewriting stops at the first unknown flag so the
// unknown-flag diagnostic stays authoritative for that invocation.
func resolveFlagValueIndirection(root *ffcli.Command, args []string) ([]string, error) {
	command := root
	sawPositional := false
	resolved := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			resolved = append(resolved, args[index:]...)
			break
		}
		if token == "" || token == "-" || !strings.HasPrefix(token, "-") {
			if token != "" && !sawPositional {
				if subcommand := findDirectSubcommand(command, token); subcommand != nil {
					command = subcommand
					resolved = append(resolved, token)
					continue
				}
				sawPositional = true
			}
			resolved = append(resolved, token)
			continue
		}

		name, inlineValue, hasInlineValue := strings.Cut(strings.TrimLeft(token, "-"), "=")
		if command == nil || command.FlagSet == nil || name == "" {
			resolved = append(resolved, args[index:]...)
			break
		}
		item := command.FlagSet.Lookup(name)
		if item == nil {
			resolved = append(resolved, args[index:]...)
			break
		}
		if isBoolFlag(item) {
			resolved = append(resolved, token)
			continue
		}
		if _, excluded := indirectionExcludedFlags[name]; excluded {
			// Consume the value token untouched so it is never mistaken for
			// a positional argument that ends subcommand descent.
			resolved = append(resolved, token)
			if !hasInlineValue && index+1 < len(args) {
				index++
				resolved = append(resolved, args[index])
			}
			continue
		}
		if hasInlineValue {
			value, err := shared.ResolveFlagValueIndirection(name, inlineValue)
			if err != nil {
				return nil, err
			}
			resolved = append(resolved, token[:len(token)-len(inlineValue)]+value)
			continue
		}
		resolved = append(resolved, token)
		if index+1 < len(args) {
			index++
			value, err := shared.ResolveFlagValueIndirection(name, args[index])
			if err != nil {
				return nil, err
			}
			resolved = append(resolved, value)
		}
	}
	return resolved, nil
}
