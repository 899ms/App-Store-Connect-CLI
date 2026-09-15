# Flag value indirection (`@env:NAME`, `@file:PATH`)

Every flag that takes a value accepts two indirection prefixes: `@env:NAME`
resolves to the named environment variable and `@file:PATH` resolves to the
contents of a file. A literal value that starts with `@` can be escaped as `@@`.
The feature keeps secrets (`--secret`, `--demo-account-password`) and long text
(`--whats-new`, JSON bodies) out of `argv`, shell history, and agent transcripts,
matching the Codemagic `cli-tools` convention that agents already know.

## Contract

- Applies to every flag on the resolved command chain (root and each
  subcommand) whose value is a separate token or an inline `--flag=value`,
  except boolean flags and the exclusion list below.
- `@env:NAME`: the value of `NAME`. Unset or empty -> usage error, exit 2,
  `Error: --flag: environment variable NAME is not set` / `is empty`.
  `@env:` with no name is a usage error.
- `@file:PATH`: the file contents with exactly one trailing line ending
  removed: the complete `\r\n` pair or a lone `\n`. A lone trailing `\r` is
  data, not a recognized line ending, and survives. Relative paths resolve
  against the working directory. The final path component must be a regular file, not a symlink,
  and at most 1 MiB. Missing, unreadable, empty, or non-regular -> usage error,
  exit 2, naming the flag and the path. `@file:` with no path is a usage error.
- `@@...`: the leading `@` is removed and the rest is passed verbatim. Values
  that start with `@` but not with `@env:`, `@file:`, or `@@` (for example
  `--channel @username`) are passed unchanged.
- Resolution happens once, before flag parsing, so repeated flags and custom
  `flag.Value` types receive the resolved text and validate it normally.
- Exclusion list (never resolved), keyed by the owning `FlagSet` rather than
  by the bare name:
  - `--output` at every level. It is the render selector on nearly every
    command and the usage renderers read it back, so it is excluded under both
    of its meanings: the format enum and the output path on `signing resign`,
    `profiles`, and the asset commands.
  - `--profile`, `--report`, and `--report-file` when owned by the root
    `FlagSet`. These select the credential profile and the CI report plumbing
    for the whole process, and the parse-failure paths re-read them from raw
    `argv` (`recoverCIReportFlags`, `recoverySuggestedRootArgs`), so resolving
    them would create two sources of truth. `recoverCIReportFlags` stops at
    the first subcommand token, so the raw-`argv` reader is itself root-scoped.
  - A command-local flag that shares one of those names is not excluded:
    `signing run --profile` is a provisioning-profile path with a single
    reader, so it resolves like any other value flag.
- Boolean flags never take an indirect value.
- Command-local flags stay resolvable even when they select a format:
  `--format` on `signing fetch`, `signing resign`, `assets previews`, and
  `assets screenshots download` is read only from its own parsed `FlagSet`, so
  the resolved value has one reader and the enum validation runs on it. The
  exclusion line is the raw-`argv` reader, not the flag's purpose.
- `--help` wins over indirection. Help rendering depends only on which command
  the args select, never on a flag value, so a help invocation drops the flag
  tokens that carry an indirect value (`dropIndirectFlagValues`, the same walk
  with resolution replaced by removal) instead of resolving them. The
  consequences are the ones worth having:
  - Help prints and exits zero whether or not the value would resolve, so an
    unset variable or a missing file never turns `--help` into an error, and a
    valid `@env:` value on a typed flag (`--limit @env:LIMIT --help`) is not
    rejected by the raw-token parse either.
  - `--help` performs no environment or filesystem read, so it stays free of
    the side effects of a value the operator did not ask to use.
  - No resolved value can reach help output or a parse diagnostic. The stdlib
    `flag` package quotes a rejected value in its error, so resolving before a
    help parse would print an environment variable's or a file's contents.
  - The precedence is over indirection only, not over flag parsing at large.
    A literal value keeps its existing behavior: `--limit=abc --help` still
    reports the invalid value, because `flag` validates in argv order.
    Neutralizing every value flag in help mode would change a documented,
    unrelated surface and hide real usage errors, so the walk drops only the
    tokens this feature introduced.
- Outside a help request a resolved value is simply the flag's value: a
  command's own validation message can quote it the same way it quotes a value
  typed on the command line (`--limit @env:NOT_A_NUMBER` reports the rejected
  text). Indirection keeps a secret out of `argv`, process listings, shell
  history, and telemetry; it is not a promise that every validator redacts a
  value the operator pointed at the wrong flag.
- Positional arguments and everything after `--` are never resolved.
- Rewriting stops at the first unknown flag token, and at a malformed
  spelling such as `---flag`, so the unknown-flag and bad-flag-syntax
  diagnostics stay authoritative for that invocation. The walk accepts the
  same prefixes as `hasValidFlagPrefix`, which is what final parsing accepts.

## Placement

The rewrite lives in `cmd.Run` as a pre-parse pass over `argv`
(`cmd/flag_indirection.go`) that walks the command tree exactly like
`normalizeSpacedBooleanFlags`. The value resolver is
`shared.ResolveFlagValueIndirection` so other entry points can reuse it.

Alternatives considered:

- Post-parse `fs.Visit` rewriting `f.Value.Set(resolved)`: rejected. Repeat-safe
  values such as `OnceCSVValue` refuse a second `Set`, `MultiStringFlag` appends,
  and typed values (`int`, `Duration`, enums) reject the literal `@env:...` at
  parse time before the pass can run.
- Opt-in per flag: rejected. Agents drive most invocations and cannot predict
  which flags support indirection; a uniform rule is the only one that is
  discoverable from the concept doc alone, and the audit found no flag whose
  legitimate values start with `@env:`, `@file:`, or `@@`.

## File access

`@file:PATH` is an operator-supplied path, not a repository-controlled or
API-supplied one, so it follows the existing `--password-file` precedent
(`certificates export`): `shared.OpenExistingNoFollow`, a regular-file check,
and a bounded read. `internal/rootfs` anchors reads and writes inside an
operator-selected root for paths the CLI derives itself; an explicit
`@file:` path has no such root, and the no-follow open plus regular-file
check are the containment the precedent applies.

## Telemetry

Telemetry never receives flag values. `EventContext.FailureParameter` carries a
flag name that `telemetry.sanitizeFailureParameter` restricts to a known
allow-list, and the event schema has no field for values. The pre-parse
error path reports `ErrorKindInvalidValue` with the flag name only;
`cmd/flag_indirection_test.go` asserts that no emitted context contains the
resolved value.

## Validation

- `internal/cli/shared/flag_indirection_test.go`: resolver cases for env, file,
  escape, and every error.
- `cmd/flag_indirection_test.go`: tree walk (subcommand descent, inline values,
  booleans, exclusion list, `--`, unknown flags, repeated and CSV flags),
  root-versus-command-local `--profile` on the real command tree, `Run` exit
  code 2 with stderr text, the help walk (`dropIndirectFlagValues` keeping
  booleans, excluded flags, unknown flags, positionals, and everything after
  `--`), help printing for unresolvable, unreadable, and resolvable indirect
  values without leaking one, and the telemetry no-leak assertion.
- `internal/cli/cmdtest/flag_indirection_test.go`: end-to-end on
  `localizations update --whats-new @file:...` and
  `webhooks create --secret @env:...`, the `@@` escape, and the error cases.
