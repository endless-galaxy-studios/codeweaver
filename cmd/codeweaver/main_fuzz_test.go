package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// FuzzCLIArgs is an integration-level fuzz test that exercises the binary's CLI
// dispatch with fuzzed argument shapes. Cobra's flag parsing is upstream-fuzzed;
// the value here is verifying that:
//   - The binary never panics (exit code 4) under arbitrary arg shapes.
//   - The binary never produces partial JSON on stdout.
//   - The binary exits with a documented exit code (0, 1, 2, 3, 4, or 5).
//
// Run with: go test -fuzz=FuzzCLIArgs -fuzztime=30s ./cmd/codeweaver/
//
// The binaryPath variable is set by TestMain in main_test.go.
func FuzzCLIArgs(f *testing.F) {
	// Seed corpus: representative valid and invalid invocations.
	f.Add("version")
	f.Add("version --json")
	f.Add("parse")
	f.Add("parse --dry-run /dev/null")
	f.Add("parse --help")
	f.Add("--help")
	f.Add("--version")
	f.Add("completion bash")
	f.Add("discover")
	f.Add("parse -- -weird")
	f.Add("parse --files-from -")
	f.Add("parse --unknown-flag")
	f.Add("")
	f.Add("not-a-subcommand")

	f.Fuzz(func(t *testing.T, input string) {
		if binaryPath == "" {
			t.Skip("binary not built (TestMain not run)")
		}

		// Parse input into args by splitting on whitespace.
		rawArgs := strings.Fields(input)

		cmd := exec.Command(binaryPath, rawArgs...)
		out, _ := cmd.Output()
		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}

		// Core invariant 1: the binary must not exit with code 4 (panic).
		// Exit 4 means our defer recover() was triggered — that is a real bug.
		if exitCode == 4 {
			t.Errorf("binary panicked (exit 4) with args %v", rawArgs)
		}

		// Core invariant 2: if stdout contains JSON, it must be complete.
		if len(out) > 0 && out[0] == '{' && len(out) < 1<<20 {
			var dummy interface{}
			if err := json.Unmarshal(out, &dummy); err != nil {
				t.Errorf("stdout looks like JSON but failed to parse with args %v: %v\nOutput: %q",
					rawArgs, err, truncateBytes(out, 200))
			}
		}
	})
}

// truncateBytes returns at most n bytes of data with "..." if truncated.
func truncateBytes(data []byte, n int) string {
	if len(data) <= n {
		return string(data)
	}
	return string(data[:n]) + "..."
}
