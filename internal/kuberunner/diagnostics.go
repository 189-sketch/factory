package kuberunner

import (
	"errors"
	"strings"
)

type commandFailure struct {
	cause   error
	hint    string
	missing bool
}

func (e *commandFailure) Error() string { return "Kubernetes command failed; " + e.hint }
func (e *commandFailure) Unwrap() error { return e.cause }

func resourceMissing(err error) bool {
	var failure *commandFailure
	return errors.As(err, &failure) && failure.missing
}

// Keep a bounded diagnostic window for classification only. Raw command output
// can contain credentials or private agent data and must never enter job errors.
type commandDiagnostics struct {
	priority int
	tail     string
	hint     string
	missing  bool
}

func (d *commandDiagnostics) Write(p []byte) (int, error) {
	const limit = 8192
	// Process large writes in windows so a useful early diagnostic is not lost.
	for offset := 0; offset < len(p); {
		end := min(offset+limit/2, len(p))
		d.tail += string(p[offset:end])
		if len(d.tail) > limit {
			d.tail = d.tail[len(d.tail)-limit:]
		}
		hint, missing, priority := diagnoseCommand(d.tail)
		// Real access failures take precedence over a missing-resource message.
		if priority > d.priority {
			d.hint, d.missing, d.priority = hint, missing, priority
		}
		offset = end
	}
	return len(p), nil
}

func diagnoseCommand(output string) (string, bool, int) {
	text := strings.ToLower(output)
	switch {
	case strings.Contains(text, "reauthentication failed") || strings.Contains(text, "gcloud auth login"):
		return "Google Cloud login has expired or is unavailable; run gcloud auth login on the runner host using the same CLOUDSDK_CONFIG as Machinist, then retry", false, 5
	case strings.Contains(text, "getting credentials: exec: executable ") && (strings.Contains(text, "not found") || strings.Contains(text, "no such file")):
		return "the kubeconfig credential plugin is missing; install the cloud provider's authentication plugin on the runner host", false, 4
	case strings.Contains(text, "(unauthorized)") || strings.Contains(text, "you must be logged in") || strings.Contains(text, "getting credentials"):
		return "cluster authentication failed; refresh the runner host's Kubernetes credentials, then retry", false, 3
	case strings.Contains(text, "(forbidden)"):
		return "the runner identity lacks Kubernetes permission; check its cluster RBAC and cloud IAM access", false, 3
	case strings.Contains(text, "error: context ") && (strings.Contains(text, "does not exist") || strings.Contains(text, "not found")) || strings.Contains(text, "error loading config file"):
		return "the configured Kubernetes context or kubeconfig is unavailable; check the runner configuration", false, 2
	case strings.Contains(text, "unable to connect to the server") || strings.Contains(text, "i/o timeout") || strings.Contains(text, "no such host") || strings.Contains(text, "connection refused") || strings.Contains(text, "x509:") || strings.Contains(text, "tls handshake timeout"):
		return "the Kubernetes API is unreachable or its certificate is invalid; check runner connectivity and cluster certificates", false, 2
	case strings.Contains(text, "error from server (notfound): namespaces "):
		return "the configured Kubernetes namespace does not exist; check the runner configuration and install its resources", false, 3
	case strings.Contains(text, "error from server (notfound)"):
		return "the requested Kubernetes resource does not exist", true, 1
	}
	return "", false, 0
}

func (d *commandDiagnostics) failure(cause error) error {
	hint := d.hint
	if hint == "" {
		hint = "check cluster access with kubectl on the runner host"
	}
	return &commandFailure{cause: cause, hint: hint, missing: d.missing}
}
