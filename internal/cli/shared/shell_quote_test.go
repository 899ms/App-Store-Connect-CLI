package shared

import (
	"flag"
	"io"
	"slices"
	"testing"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "safe word stays bare", value: "release-2026.1", want: "release-2026.1"},
		{name: "path stays bare", value: "/tmp/AuthKey_ABC123.p8", want: "/tmp/AuthKey_ABC123.p8"},
		{name: "empty value", value: "", want: "''"},
		{name: "spaces", value: "My Key", want: "'My Key'"},
		{name: "command substitution stays inert", value: "$(whoami)", want: "'$(whoami)'"},
		{name: "backticks stay inert", value: "`id`", want: "'`id`'"},
		{name: "tilde is not expanded", value: "~/keys", want: "'~/keys'"},
		{name: "embedded single quote", value: "it's", want: `'it'\''s'`},
		{name: "control characters are escaped", value: "a\x1b[31mred\nb", want: `$'a\x1b[31mred\nb'`},
		{name: "control characters with quotes", value: "a\t\"b\"'c'", want: `$'a\t"b"\'c\''`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ShellQuote(test.value); got != test.want {
				t.Fatalf("ShellQuote(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestShellQuoteNeverEmitsRawControlCharacters(t *testing.T) {
	for _, value := range []string{"a\x1b]0;title\x07b", "line\r\nnext", "bell\a"} {
		quoted := ShellQuote(value)
		for _, r := range quoted {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("ShellQuote(%q) = %q leaked control rune %U", value, quoted, r)
			}
		}
	}
}

func TestRootFlagsForReinvocation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "no root flags", args: []string{}, want: []string{}},
		{name: "profile", args: []string{"--profile", "release"}, want: []string{"--profile", "release"}},
		{name: "profile is quoted", args: []string{"--profile", "my key"}, want: []string{"--profile", "'my key'"}},
		{name: "strict auth", args: []string{"--strict-auth"}, want: []string{"--strict-auth"}},
		{
			name: "report flags",
			args: []string{"--profile", "release", "--report", "junit", "--report-file", "out dir/report.xml"},
			want: []string{"--profile", "release", "--report", "junit", "--report-file", "'out dir/report.xml'"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// BindRootFlags rebinds the package-level root flag state, so each
			// case starts from the flag defaults.
			fs := flag.NewFlagSet("asc", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			BindRootFlags(fs)
			t.Cleanup(func() {
				restore := flag.NewFlagSet("asc", flag.ContinueOnError)
				restore.SetOutput(io.Discard)
				BindRootFlags(restore)
			})
			if err := fs.Parse(test.args); err != nil {
				t.Fatalf("Parse() error: %v", err)
			}

			if got := RootFlagsForReinvocation(); !slices.Equal(got, test.want) {
				t.Fatalf("RootFlagsForReinvocation() = %q, want %q", got, test.want)
			}
		})
	}
}
