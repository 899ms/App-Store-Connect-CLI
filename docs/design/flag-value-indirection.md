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
  (`\n` or `\r\n`) removed. Relative paths resolve against the working
  directory. The final path component must be a regular file, not a symlink,
  and at most 1 MiB. Missing, unreadable, empty, or non-regular -> usage error,
  exit 2, naming the flag and the path. `@file:` with no path is a usage error.
- `@@...`: the leading `@` is removed and the rest is passed verbatim. Values
  that start with `@` but not with `@env:`, `@file:`, or `@@` (for example
  `--channel @username`) are passed unchanged.
- Resolution happens once, before flag parsing, so repeated flags and custom
  `flag.Value` types receive the resolved text and validate it normally.
- Exclusion list (never resolved): `--output`, `--profile`, `--report`,
  `--report-file`. These select how `asc` itself runs and are read from raw
  `argv` on parse-failure paths (`recoverCIReportFlags`, the usage renderers),
  so resolving them would create two sources of truth. `--output` is excluded
  under both of its meanings, the format enum and the output path on
  `signing resign`, `profiles`, and the asset commands. Boolean flags never
  take an indirect value.
- Command-local flags stay resolvable even when they select a format:
  `--format` on `signing fetch`, `signing resign`, `assets previews`, and
  `assets screenshots download` is read only from its own parsed `FlagSet`, so
  the resolved value has one reader and the enum validation runs on it. The
  exclusion line is the raw-`argv` reader, not the flag's purpose.
- Positional arguments and everything after `--` are never resolved.
- Rewriting stops at the first unknown flag token so the unknown-flag
  diagnostic stays authoritative for that invocation.

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
  `Run` exit code 2 with stderr text, and the telemetry no-leak assertion.
- `internal/cli/cmdtest/flag_indirection_test.go`: end-to-end on
  `localizations update --whats-new @file:...` and
  `webhooks create --secret @env:...`, the `@@` escape, and the error cases.
