package runner

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type nativeInput struct {
	strings.Builder
	closed bool
}

func (i *nativeInput) Close() error { i.closed = true; return nil }
func testNative(t *testing.T, adapter string) (*nativeStream, *nativeInput, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := newEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.close() })
	input := &nativeInput{}
	return &nativeStream{adapter: adapter, log: log, stdin: input, root: "primary"}, input, path
}
func feedNative(t *testing.T, n *nativeStream, text string) {
	t.Helper()
	for _, part := range []string{text[:len(text)/2], text[len(text)/2:]} {
		if _, err := io.WriteString(n, part); err != nil {
			t.Fatal(err)
		}
	}
}
func TestNativeCodexChildLifecycleAndPrimary(t *testing.T) {
	n, input, path := testNative(t, "codex-app-server")
	feedNative(t, n, `{"method":"item/started","params":{"threadId":"primary","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child1","agentPath":"/root/read"}}}`+"\n")
	feedNative(t, n, `{"method":"item/completed","params":{"threadId":"primary","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child1"}}}`+"\n")
	if n.active["child1"].Status != "running" {
		t.Fatal("start notification completed child")
	}
	feedNative(t, n, `{"method":"item/started","params":{"threadId":"primary","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child2","agentPath":"/root/read"}}}`+"\n")
	feedNative(t, n, `{"method":"item/started","params":{"threadId":"primary","item":{"type":"subAgentActivity","kind":"failed","agentThreadId":"child1"}}}`+"\n")
	feedNative(t, n, `{"method":"item/completed","params":{"threadId":"child1","item":{"type":"agentMessage","text":"CHILD"}}}`+"\n")
	feedNative(t, n, `{"method":"turn/completed","params":{"threadId":"child1","turn":{"status":"failed"}}}`+"\n")
	if input.closed || n.final != "" {
		t.Fatal("child completed primary")
	}
	feedNative(t, n, `{"method":"item/completed","params":{"threadId":"primary","item":{"type":"agentMessage","text":"PARENT"}}}`+"\n")
	feedNative(t, n, `{"method":"turn/completed","params":{"threadId":"primary","turn":{"status":"completed"}}}`+"\n")
	if !input.closed || n.final != "PARENT" || len(n.active) != 2 {
		t.Fatalf("bad parent state: %+v", n)
	}
	n.finish(false)
	if n.active["child2"].Status != "unknown" {
		t.Fatal("unobserved completion fabricated")
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "agent.activity") {
		t.Fatal("activity not written live")
	}
}
func TestNativeClaudeFailureIsOwnedByPrimary(t *testing.T) {
	n, _, _ := testNative(t, "claude-stream")
	for _, line := range []string{`{"type":"system","subtype":"task_started","task_id":"child1","task_type":"local_agent","description":"Reader"}`, `{"type":"system","subtype":"task_notification","task_id":"child1","status":"failed","summary":"read failed"}`, `{"type":"result","parent_tool_use_id":"child1","is_error":true,"result":"CHILD"}`, `{"type":"result","is_error":false,"result":"PARENT"}`} {
		feedNative(t, n, line+"\n")
	}
	if n.final != "PARENT" || n.active["child1"].Status != "failed" {
		t.Fatal("child failure escaped")
	}
}
func TestNativeInterruptAndUnknownEvents(t *testing.T) {
	n, _, _ := testNative(t, "claude-stream")
	feedNative(t, n, "bad json\n{\"type\":\"future\",\"v\":9}\n")
	_ = n.observe(Activity{NativeID: "child", Status: "running"})
	n.finish(true)
	if n.active["child"].Status != "interrupted" {
		t.Fatal("child not interrupted")
	}
}
func TestNativeResultSmallContract(t *testing.T) {
	result, err := parseNativeResult(`{"outcome":"complete","summary":"answer"}`)
	if err != nil || result.Factory.Role != "primary" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err = parseNativeResult(`{"outcome":"complete","summary":""}`); err == nil {
		t.Fatal("empty summary accepted")
	}
}
func TestNativeRPCPreflight(t *testing.T) {
	n, input, _ := testNative(t, "codex-app-server")
	n.model = "unavailable"
	if err := n.start(); err != nil {
		t.Fatal(err)
	}
	feedNative(t, n, "{\"id\":1,\"result\":{}}\n")
	feedNative(t, n, "{\"id\":2,\"result\":{\"account\":{\"type\":\"chatgpt\"}}}\n")
	if err := n.line([]byte(`{"id":3,"result":{"data":[{"id":"available"}]}}`)); err == nil {
		t.Fatal("missing model accepted")
	}
	if strings.Contains(input.String(), "thread/start") {
		t.Fatal("started before model validation")
	}
	var v any
	if json.Unmarshal([]byte(strings.Split(input.String(), "\n")[0]), &v) != nil {
		t.Fatal("invalid RPC")
	}
}

func TestNativeOversizedActivityFitsTransport(t *testing.T) {
	n, _, path := testNative(t, "claude-stream")
	body, _ := json.Marshal(map[string]any{"type": "system", "subtype": "task_notification", "task_id": "child", "status": "completed", "summary": strings.Repeat("x", 200<<10)})
	feedNative(t, n, string(body)+"\n")
	content, _ := os.ReadFile(path)
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if len(line) > 128<<10 {
			t.Fatalf("unshippable normalized event: %d", len(line))
		}
	}
}

func TestNativeEOFErrorIsNotSuccess(t *testing.T) {
	n, _, _ := testNative(t, "claude-stream")
	if _, err := io.WriteString(n, `{"type":"result","is_error":true,"result":"failed"}`); err != nil {
		t.Fatal(err)
	}
	if err := n.finish(false); err == nil || n.completed {
		t.Fatal("EOF error lost")
	}
}
func TestNativeEscapedActivityFitsTransport(t *testing.T) {
	n, _, path := testNative(t, "claude-stream")
	if err := n.observe(Activity{NativeID: strings.Repeat("\x00", 2000), Name: strings.Repeat("\x00", 10000), Status: strings.Repeat("\x00", 200000), Summary: strings.Repeat("\x00", 200000), ParentToolUseID: strings.Repeat("\x00", 2000)}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if len(body) > 128<<10 {
		t.Fatalf("unshippable escaped record %d", len(body))
	}
}

func TestNativeDefaultModelTravelsInRequest(t *testing.T) {
	n, input, _ := testNative(t, "codex-app-server")
	if err := n.line([]byte(`{"id":3,"result":{"data":[{"id":"account-default","model":"account-default","isDefault":true}]}}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.String(), `"model":"account-default"`) {
		t.Fatalf("default absent from request: %s", input.String())
	}
}

func TestClaudeOnlyKnownAgentsAndStableLabels(t *testing.T) {
	n, _, path := testNative(t, "claude-stream")
	for _, line := range []string{
		`{"type":"system","subtype":"task_started","task_id":"shell","task_type":"local_bash","description":"npm test"}`,
		`{"type":"system","subtype":"task_started","task_id":"reader","task_type":"local_agent","description":"Review code","tool_use_id":"tool-reader"}`,
		`{"type":"system","subtype":"task_progress","task_id":"reader","description":"Running git diff"}`,
		`{"type":"system","subtype":"task_notification","task_id":"shell","status":"completed"}`,
		`{"type":"assistant","agent_id":"reader","parent_tool_use_id":"tool-reader","message":{"content":[{"type":"text","text":"Report"}]}}`,
		`{"type":"system","subtype":"task_notification","task_id":"reader","status":"completed","description":"Running tests","summary":"Accepted"}`,
	} {
		feedNative(t, n, line+"\n")
	}
	if len(n.active) != 1 || n.active["reader"].Name != "Review code" || n.active["reader"].Status != "completed" {
		t.Fatalf("bad projection: %+v", n.active)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `\"native_id\":\"reader\"`) || strings.Contains(string(raw), `\"native_id\":\"tool-reader\"`) {
		t.Fatal("wrong child message identity")
	}
}
func TestCodexInteractionDoesNotCreateRootOrResetLifecycle(t *testing.T) {
	n, _, _ := testNative(t, "codex-app-server")
	for _, item := range []string{
		`{"type":"subAgentActivity","kind":"started","agentThreadId":"child","agentPath":"/root/reviewer"}`,
		`{"type":"subAgentActivity","kind":"interacted","agentThreadId":"child"}`,
		`{"type":"subAgentActivity","kind":"interacted","agentThreadId":"primary","agentPath":"/root"}`,
	} {
		feedNative(t, n, `{"method":"item/started","params":{"item":`+item+`}}`+"\n")
	}
	if len(n.active) != 1 || n.active["child"].Status != "running" {
		t.Fatalf("bad interaction: %+v", n.active)
	}
}
