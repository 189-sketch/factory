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

func TestNativeSpoolSeeksAcknowledgedPrefix(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls++
		var batch protocol.RunEventBatch
		_ = json.NewDecoder(r.Body).Decode(&batch)
		var event protocol.RunEvent
		_ = json.Unmarshal(batch.Events[len(batch.Events)-1], &event)
		_ = json.NewEncoder(out).Encode(protocol.RunEventPage{Cursor: event.Sequence})
	}))
	defer server.Close()
	worker := &Worker{config: config.Worker{DataDirectory: t.TempDir()}, instanceID: "worker", client: newClient(server.URL, "token", server.Client())}
	spec := protocol.RunSpec{ID: "run", LeaseToken: "lease"}
	path := worker.eventSpool(spec)
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	first := "{\"sequence\":1,\"type\":\"run.started\"}\n"
	_ = os.WriteFile(path, []byte(first), 0600)
	// Existing numeric acknowledgments are migrated after one prefix scan.
	_ = os.WriteFile(path+".ack", []byte("1"), 0600)
	if err := worker.flushRunEvents(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	// An already acknowledged prefix is not read or parsed again.
	file, _ := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0600)
	_, _ = file.WriteString("{\"sequence\":2,\"type\":\"run.completed\"}\n")
	file.Close()
	file, _ = os.OpenFile(path, os.O_RDWR, 0600)
	_, _ = file.WriteAt([]byte("BROKEN"), 0)
	file.Close()
	if err := worker.flushRunEvents(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	var ack struct{ Cursor, Offset int64 }
	raw, _ := os.ReadFile(path + ".ack")
	_ = json.Unmarshal(raw, &ack)
	info, _ := os.Stat(path)
	if calls != 1 || ack.Cursor != 2 || ack.Offset != info.Size() {
		t.Fatalf("bad checkpoint: calls=%d ack=%+v", calls, ack)
	}
}
