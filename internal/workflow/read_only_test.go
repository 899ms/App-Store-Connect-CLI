package workflow

import (
	"slices"
	"testing"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/readonly"
)

func TestBuildEnvSliceForcesReadOnlyIntoSteps(t *testing.T) {
	t.Setenv("ASC_WORKFLOW_ENV_PROBE", "probe")
	t.Setenv(readonly.EnvVar, "")

	if got := buildEnvSlice(nil); slices.Contains(got, readonly.EnvVar+"=1") {
		t.Fatalf("step environment carries %s=1 while read-only mode is off", readonly.EnvVar)
	}

	t.Setenv(readonly.EnvVar, "1")
	for _, env := range []map[string]string{
		nil,
		{"STEP_KEY": "step-value"},
		// A step's declared env must not override the session policy.
		{readonly.EnvVar: "0"},
	} {
		got := buildEnvSlice(env)
		if count := countEnvKey(got, readonly.EnvVar); count != 1 {
			t.Fatalf("step environment has %d %s entries, want 1: %v", count, readonly.EnvVar, got)
		}
		if got[len(got)-1] != readonly.EnvVar+"=1" {
			t.Fatalf("last step environment entry = %q, want %s=1", got[len(got)-1], readonly.EnvVar)
		}
		if !slices.Contains(got, "ASC_WORKFLOW_ENV_PROBE=probe") {
			t.Fatal("step environment dropped an inherited variable")
		}
	}
}

func countEnvKey(entries []string, key string) int {
	count := 0
	for _, entry := range entries {
		if len(entry) > len(key) && entry[:len(key)+1] == key+"=" {
			count++
		}
	}
	return count
}
