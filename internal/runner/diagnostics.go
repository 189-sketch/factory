package runner

import (
	"strings"
	"sync"
)

// startupDiagnostics recognizes actionable CLI failures without copying raw
// process output (which may contain credentials or repository data) to GitHub.
type startupDiagnostics struct {
	mu   sync.Mutex
	tail string
	hint string
}

func (d *startupDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tail += string(p)
	if len(d.tail) > 8192 {
		d.tail = d.tail[len(d.tail)-8192:]
	}
	text := strings.ToLower(d.tail)
	switch {
	case strings.Contains(text, "readonly database") || strings.Contains(text, "read-only database"):
		d.hint = "The agent could not write its local runtime state. Give the worker a writable agent data directory, then retry."
	case strings.Contains(text, "failed to initialize in-process app-server client"):
		if d.hint == "" {
			d.hint = "Codex could not start its local runtime. Check worker permissions and the Codex data directory, then retry."
		}
	case strings.Contains(text, "model is not supported") || strings.Contains(text, "model does not exist"):
		d.hint = "The configured model is unavailable for this account. Choose a supported model in the worker configuration, or use the CLI default, then retry."
	case strings.Contains(text, "authentication required") || strings.Contains(text, "not logged in") || strings.Contains(text, "invalid api key"):
		d.hint = "The agent is not authenticated. Sign in to its CLI on the worker host, then retry."
	}
	return len(p), nil
}

func (d *startupDiagnostics) message() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hint
}
