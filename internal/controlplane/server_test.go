package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/factory-v2/internal/config"
	"github.com/owainlewis/factory-v2/internal/protocol"
	"github.com/owainlewis/factory-v2/internal/runner"
)

func TestServerProtectsSubmissionAndWorkerAPIs(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	status := getStatus(t, webServer.URL)
	if status.CSRFToken == "" || len(status.Agents) != 1 || status.Agents[0] != "plan" {
		t.Fatalf("status = %#v", status)
	}

	unauthorized := postJSON(t, webServer.URL+"/api/v1/workers/poll", map[string]any{}, nil)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()

	foreignHeaders := map[string]string{"Origin": "https://evil.example", "X-Factory-CSRF": status.CSRFToken}
	foreign := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{"prompt": "Work locally", "repository": "factory", "agent": "plan"}, foreignHeaders)
	if foreign.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign submission status = %d", foreign.StatusCode)
	}
	foreign.Body.Close()

	headers := map[string]string{"Origin": webServer.URL, "X-Factory-CSRF": status.CSRFToken}
	created := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{"prompt": "Work locally", "repository": "factory", "agent": "plan"}, headers)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", created.StatusCode)
	}
	created.Body.Close()

	status = getStatus(t, webServer.URL)
	if len(status.Jobs) != 1 || status.Jobs[0].Prompt != "Work locally" || status.Jobs[0].Runs[0].State != "queued" {
		t.Fatalf("jobs = %#v", status.Jobs)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"prompt":"x"}`))
	request.Host = "127.0.0.1:7331"
	request.Header.Set("Origin", "http://127.0.0.1:7331")
	request.Header.Set("X-Factory-CSRF", server.csrfToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid selection status = %d", response.Code)
	}
}

func TestServerServesEmbeddedReactAppAndRejectsRemoteListen(t *testing.T) {
	_, webServer := newTestHTTPServer(t)
	defer webServer.Close()
	response, err := http.Get(webServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(body.String(), `<div id="root"></div>`) {
		t.Fatalf("status = %d body = %q", response.StatusCode, body.String())
	}
	if err := validateLoopbackListen("0.0.0.0:7331"); err == nil {
		t.Fatal("expected non-loopback listen rejection")
	}
	if err := validateLoopbackListen("127.0.0.1:7331"); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionLimitFitsMaximumRecordedOutput(t *testing.T) {
	// encoding/json can double a string when every byte requires escaping.
	worstCaseEncodedEvents := 2 * runner.MaxEventLogBytes
	const envelopeAllowance = 4 << 20
	if maxCompletionBytes < worstCaseEncodedEvents+envelopeAllowance {
		t.Fatalf("completion limit %d cannot hold encoded events and envelope %d", maxCompletionBytes, worstCaseEncodedEvents+envelopeAllowance)
	}
}

func TestHeartbeatEndpointAuthenticatesAndRejectsInvalidLeases(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()
	auth := map[string]string{"Authorization": "Bearer secret"}

	unauthorized := postJSON(t, webServer.URL+"/api/v1/runs/missing/heartbeat", protocol.Heartbeat{}, nil)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized heartbeat status = %d", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()
	unknown := postJSON(t, webServer.URL+"/api/v1/runs/missing/heartbeat", protocol.Heartbeat{InstanceID: "worker-a", LeaseToken: "lease"}, auth)
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown heartbeat status = %d", unknown.StatusCode)
	}
	unknown.Body.Close()

	if _, err := server.store.CreateJob(t.Context(), "request", "factory", "agent", "plan", []config.ResolvedAgent{testAgent("plan", "Plan request")}); err != nil {
		t.Fatal(err)
	}
	pollResponse := postJSON(t, webServer.URL+"/api/v1/workers/poll", pollRequest("worker-a", []string{"codex"}, []string{"factory"}), auth)
	if pollResponse.StatusCode != http.StatusOK {
		t.Fatalf("poll status = %d", pollResponse.StatusCode)
	}
	var polled protocol.PollResponse
	if err := json.NewDecoder(pollResponse.Body).Decode(&polled); err != nil {
		t.Fatal(err)
	}
	pollResponse.Body.Close()
	if polled.Run == nil {
		t.Fatal("poll returned no run")
	}

	for name, heartbeat := range map[string]protocol.Heartbeat{
		"other worker": {InstanceID: "worker-b", LeaseToken: polled.Run.LeaseToken},
		"stale token":  {InstanceID: "worker-a", LeaseToken: "stale"},
	} {
		t.Run(name, func(t *testing.T) {
			response := postJSON(t, webServer.URL+"/api/v1/runs/"+polled.Run.ID+"/heartbeat", heartbeat, auth)
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d", response.StatusCode)
			}
			response.Body.Close()
		})
	}
	validHeartbeat := protocol.Heartbeat{InstanceID: "worker-a", LeaseToken: polled.Run.LeaseToken}
	renewed := postJSON(t, webServer.URL+"/api/v1/runs/"+polled.Run.ID+"/heartbeat", validHeartbeat, auth)
	if renewed.StatusCode != http.StatusNoContent {
		t.Fatalf("valid heartbeat status = %d", renewed.StatusCode)
	}
	renewed.Body.Close()

	completion := protocol.Completion{InstanceID: "worker-a", LeaseToken: polled.Run.LeaseToken, State: "succeeded", ExitCode: 0}
	completed := postJSON(t, webServer.URL+"/api/v1/runs/"+polled.Run.ID+"/complete", completion, auth)
	if completed.StatusCode != http.StatusNoContent {
		t.Fatalf("completion status = %d", completed.StatusCode)
	}
	completed.Body.Close()
	nonRunning := postJSON(t, webServer.URL+"/api/v1/runs/"+polled.Run.ID+"/heartbeat", validHeartbeat, auth)
	if nonRunning.StatusCode != http.StatusConflict {
		t.Fatalf("non-running heartbeat status = %d", nonRunning.StatusCode)
	}
	nonRunning.Body.Close()
}

func newTestHTTPServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "plan.md")
	if err := os.WriteFile(promptPath, []byte("Plan this request:\n{{factory.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(definitionPath, []byte("[agents.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\ntimeout = \"1m\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, filepath.Join(directory, "factory.db"))
	server, err := NewServer(store, definitionPath, "secret")
	if err != nil {
		t.Fatal(err)
	}
	return server, httptest.NewServer(server.Handler())
}

func getStatus(t *testing.T, endpoint string) statusResponse {
	t.Helper()
	response, err := http.Get(endpoint + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var status statusResponse
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func postJSON(t *testing.T, endpoint string, body any, headers map[string]string) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
