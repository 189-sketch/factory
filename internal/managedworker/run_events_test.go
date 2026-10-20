package managedworker

import (
	"context"
	"encoding/json"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeSpoolLostAckReplayAndPartialLine(t *testing.T) {
	events := map[int64]bool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls++
		var batch protocol.RunEventBatch
		if e := json.NewDecoder(r.Body).Decode(&batch); e != nil {
			t.Error(e)
		}
		var cursor int64
		for _, raw := range batch.Events {
			var event protocol.RunEvent
			_ = json.Unmarshal(raw, &event)
			events[event.Sequence] = true
			cursor = event.Sequence
		}
		if calls == 1 {
			http.Error(out, "ack lost", 503)
			return
		}
		_ = json.NewEncoder(out).Encode(protocol.RunEventPage{Cursor: cursor})
	}))
	defer server.Close()
	worker := &Worker{config: config.Worker{DataDirectory: t.TempDir()}, instanceID: "worker", client: newClient(server.URL, "token", server.Client())}
	spec := protocol.RunSpec{ID: "run", LeaseToken: "lease"}
	path := worker.eventSpool(spec)
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte("{\"sequence\":1,\"type\":\"agent.activity\"}\n{\"sequence\":2,\"type\":\"agent.message\"}"), 0600)
	if err := worker.flushRunEvents(context.Background(), spec); err == nil {
		t.Fatal("lost ack accepted")
	}
	if err := worker.flushRunEvents(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || calls != 2 {
		t.Fatalf("partial event or no replay: %v %d", events, calls)
	}
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = file.WriteString("\n")
	file.Close()
	if err := worker.flushRunEvents(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatal("partial line not retried")
	}
	if err := worker.flushRunEvents(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("acked events resent")
	}
}
