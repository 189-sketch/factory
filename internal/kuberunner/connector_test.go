package kuberunner

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
)

const testAuth = `{"auth_mode":"chatgpt","tokens":{"access_token":"fictional-access","refresh_token":"fictional-refresh"}}`

func configuration() config.Kubernetes {
	return config.Kubernetes{Context: "test", Namespace: "runners", Image: "example.test/runner@sha256:" + strings.Repeat("a", 64), AuthSecret: "codex-test", ProxyURL: "http://egress:3128"}
}

func TestSubscriptionAuthDoesNotFallBackToAPI(t *testing.T) {
	if err := ValidateAuth([]byte(testAuth)); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `{"auth_mode":"apikey","OPENAI_API_KEY":"fictional"}`, `{"auth_mode":"chatgptAuthTokens","tokens":{"access_token":"a","refresh_token":"b"}}`, strings.Repeat("x", 65537)} {
		if ValidateAuth([]byte(body)) == nil {
			t.Fatal("accepted non-native subscription auth")
		}
	}
}

func TestReceiveRejectsPathsLinksAndDuplicateInputs(t *testing.T) {
	for _, headers := range [][]tar.Header{{{Name: "../auth.json", Typeflag: tar.TypeReg}}, {{Name: "request.json", Typeflag: tar.TypeSymlink, Linkname: "/credentials/auth.json"}}, {{Name: "request.json", Typeflag: tar.TypeReg}, {Name: "request.json", Typeflag: tar.TypeReg}}} {
		var b bytes.Buffer
		w := tar.NewWriter(&b)
		for _, h := range headers {
			if err := w.WriteHeader(&h); err != nil {
				t.Fatal(err)
			}
		}
		w.Close()
		dir := t.TempDir()
		if Receive(&b, dir) == nil {
			t.Fatal("accepted unsafe input")
		}
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			t.Fatal("unsafe input marked ready")
		}
	}
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, name := range []string{"request.json", "input.bundle"} {
		_ = w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 2})
		_, _ = w.Write([]byte("{}"))
	}
	w.Close()
	if err := Receive(&b, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestJobKeepsAuthorityOutsideSandbox(t *testing.T) {
	c := Connector{Config: configuration()}
	object := c.manifest("test-job", true, 300)
	b, _ := json.Marshal(object)
	text := string(b)
	for _, forbidden := range []string{"GH_TOKEN", "GITHUB_TOKEN", "hostPath", "privileged", "hostNetwork", "lease_token", "OPENAI_API_KEY", "control_plane"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unexpected authority: %s", forbidden)
		}
	}
	spec := object["spec"].(map[string]any)
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	if pod["runtimeClassName"] != "gvisor" || pod["automountServiceAccountToken"] != false || spec["backoffLimit"] != 0 || pod["restartPolicy"] != "Never" {
		t.Fatal("unsafe lifecycle")
	}
	agent := pod["containers"].([]any)[0].(map[string]any)
	if agent["workingDir"] != SourceDirectory || filepath.Dir(SourceDirectory) != "/source" {
		t.Fatal("agent must use an init-created checkout inside the volume, not its root-owned mount")
	}
	if !strings.Contains(string(mustJSON(agent["volumeMounts"])), `"mountPath":"/source","name":"source","readOnly":true`) {
		t.Fatal("review source is writable")
	}
	init := pod["initContainers"].([]any)[0].(map[string]any)
	if strings.Contains(string(mustJSON(init)), "codex-auth") {
		t.Fatal("preparation received model credentials")
	}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestProxyDeniesUnapprovedAndInternalDestinations(t *testing.T) {
	h := ProxyHandler([]string{"chatgpt.com"})
	for _, host := range []string{"example.com:443", "169.254.169.254:443", "chatgpt.com:80", "chatgpt.com.evil.test:443"} {
		r := httptest.NewRequest("CONNECT", "http://"+host, nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("allowed %s", host)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "::ffff:192.168.1.1"} {
		if PublicAddress(netip.MustParseAddr(ip)) {
			t.Fatal("accepted internal address", ip)
		}
	}
	if !PublicAddress(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("denied public address")
	}
}

func TestCancellationRetainsAuthAndWaitsForPodDeletion(t *testing.T) {
	for _, podGone := range []bool{false, true} {
		t.Run(fmt.Sprint(podGone), func(t *testing.T) {
			repo := t.TempDir()
			for _, args := range [][]string{{"init"}, {"-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "initial"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = repo
				if b, e := cmd.CombinedOutput(); e != nil {
					t.Fatalf("git: %v %s", e, b)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopping, retained, deleted, unlocked := false, false, false, false
			c := Connector{Config: configuration()}
			c.run = func(callContext context.Context, input io.Reader, limit int, args ...string) ([]byte, error) {
				if callContext.Err() != nil {
					return nil, callContext.Err()
				}
				line := strings.Join(args, " ")
				if strings.HasPrefix(line, "get runtimeclass ") || strings.HasPrefix(line, "get networkpolicy ") {
					return []byte(`{}`), nil
				}
				if strings.HasPrefix(line, "get deployment ") {
					return []byte(`{"status":{"availableReplicas":1}}`), nil
				}
				if args[0] == "create" {
					body, _ := io.ReadAll(input)
					if bytes.Contains(body, []byte("fictional-refresh")) {
						t.Fatal("credential entered Job")
					}
					return []byte("created"), nil
				}
				if strings.HasPrefix(line, "get pods ") {
					if deleted {
						if podGone {
							return []byte(`{"items":[]}`), nil
						}
						return []byte(`{"items":[{}]}`), nil
					}
					return []byte(`{"items":[{"metadata":{"name":"test-pod"},"status":{"initContainerStatuses":[{"state":{"running":{}}}]}}]}`), nil
				}
				if strings.Contains(line, "/exchange/stop") {
					stopping = true
					return nil, nil
				}
				if strings.Contains(line, "test -f /exchange/done") {
					if stopping {
						return nil, nil
					}
					cancel()
					return nil, context.Canceled
				}
				if strings.Contains(line, "cat /home/agent/.codex/auth.json") {
					if !stopping {
						t.Fatal("read cache before stopping")
					}
					return []byte(testAuth), nil
				}
				if strings.HasPrefix(line, "get secret ") {
					return []byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"codex-test","resourceVersion":"1"}}`), nil
				}
				if args[0] == "replace" {
					retained = true
					return nil, nil
				}
				if strings.HasPrefix(line, "delete job ") {
					if !strings.Contains(line, "--cascade=foreground") {
						t.Fatal("background deletion")
					}
					if !retained {
						t.Fatal("deleted before retaining auth")
					}
					deleted = true
					return nil, nil
				}
				if strings.HasPrefix(line, "delete configmap ") {
					unlocked = true
					return nil, nil
				}
				if args[0] == "exec" {
					return nil, nil
				}
				return nil, fmt.Errorf("unexpected command %s", line)
			}
			result, _, err := c.Execute(ctx, runner.Options{Repository: repo, RunID: "run_0123456789abcdef01234567", ArtifactKey: "lease_test", DataDirectory: t.TempDir(), Command: config.ResolvedCommand{Timeout: time.Minute}}, true)
			if result.ProcessStopped != podGone || result.ID != "run_0123456789abcdef01234567" || result.State != runner.StateCancelled {
				t.Fatalf("cancellation lost cleanup evidence: state=%s stopped=%v", result.State, result.ProcessStopped)
			}
			if err == nil || !retained || !deleted || unlocked != podGone {
				t.Fatalf("err=%v retained=%v deleted=%v unlocked=%v", err, retained, deleted, unlocked)
			}
		})
	}
}

func TestInstallAllowsGoProxyDownloads(t *testing.T) {
	data, _ := json.Marshal((Connector{}).InstallObjects())
	if !strings.Contains(string(data), "storage.googleapis.com") {
		t.Fatal("Go module archive host is blocked")
	}
}

func TestConnectorBeforeJobFailureHasNoOutstandingProcess(t *testing.T) {
	result, _, err := (Connector{}).Execute(t.Context(), runner.Options{RunID: "not-started"}, true)
	if err == nil || !result.ProcessStopped || result.ID != "" {
		t.Fatal("preflight failure must not invent an active attempt")
	}
}
