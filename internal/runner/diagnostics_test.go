package runner

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestAgentStartupFailureExplainsRecoveryWithoutPublishingRawOutput(t *testing.T) {
	for _, tc := range []struct{ output, want string }{
		{"failed to open state DB: attempt to write a readonly database", "writable agent data directory"},
		{"failed to initialize in-process app-server client: Operation not permitted", "Codex could not start"},
		{"The configured model is not supported when using Codex with a ChatGPT account.", "configured model is unavailable"},
		{"Authentication required", "Sign in to its CLI"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			agent := codexJSONAgentCommand(t, "codex", "codex", false, "cat >/dev/null; printf '%s' 'secret-test-value "+tc.output+"' >&2; exit 1", 5*time.Second)
			_, err := Execute(t.Context(), Options{Command: agent, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-test-value") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDiagnosticsHandlesChunkBoundariesAndBoundsStoredOutput(t *testing.T) {
	d := &startupDiagnostics{}
	_, _ = d.Write([]byte(strings.Repeat("x", 20000)))
	_, _ = d.Write([]byte("attempt to write a read"))
	_, _ = d.Write([]byte("only database"))
	if len(d.tail) > 8192 || !strings.Contains(d.message(), "writable") {
		t.Fatalf("diagnostic missing or unbounded: %q", d.message())
	}
}

func TestDiagnosticsIgnoreQuotedErrorsInAgentAndCommandOutput(t *testing.T) {
	d := &startupDiagnostics{}
	collector := newUsageCollector("codex", []string{"codex", "exec", "--json"})
	collector.diagnostics = d
	_, _ = collector.Write([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"The sample said invalid api key"}}` + "\n" + `{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"readonly database"}}` + "\n"))
	if d.message() != "" {
		t.Fatal("quoted task or tool output became a startup error")
	}
	_, _ = collector.Write([]byte(`{"type":"turn.failed","error":{"message":"The model is not supported"}}` + "\n"))
	if !strings.Contains(d.message(), "model is unavailable") {
		t.Fatal("structured CLI failure not recognized")
	}
}
