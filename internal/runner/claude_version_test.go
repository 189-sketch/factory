package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestrictedClaudeRequiresSupportedExecutionHostVersion(t *testing.T) {
	for _, version := range []string{"2.1.247", "2.1.248", "2.2.0", "3.0.0", "unknown"} {
		t.Run(version, func(t *testing.T) {
			executable := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s' '"+version+" (Claude Code)'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			err := CheckRestrictedClaude(t.Context(), []string{executable, "--restricted"})
			supported := version != "2.1.247" && version != "unknown"
			if (err == nil) != supported {
				t.Fatalf("version %s: %v", version, err)
			}
			if err != nil && !strings.Contains(err.Error(), "claude update") {
				t.Fatal("missing recovery instruction")
			}
		})
	}
}
