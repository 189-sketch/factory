package managedworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func (w *Worker) eventSpool(spec protocol.RunSpec) string {
	return filepath.Join(w.config.DataDirectory, "runs", spec.ID, spec.LeaseToken, "events.jsonl")
}

// The JSONL file is the spool. A torn last line is retried after the next write.
func (w *Worker) flushRunEvents(ctx context.Context, spec protocol.RunSpec) error {
	path := w.eventSpool(spec)
	cursorPath := path + ".ack"
	var cursor int64
	if body, err := os.ReadFile(cursorPath); err == nil {
		_ = json.Unmarshal(body, &cursor)
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 128<<10)
	batch := protocol.RunEventBatch{InstanceID: w.instanceID, LeaseToken: spec.LeaseToken}
	bytes := 0
	send := func() error {
		if len(batch.Events) == 0 {
			return nil
		}
		var page protocol.RunEventPage
		if err := w.client.Post(ctx, "/api/v1/runs/"+spec.ID+"/events", batch, &page); err != nil {
			return err
		}
		var last protocol.RunEvent
		_ = json.Unmarshal(batch.Events[len(batch.Events)-1], &last)
		if page.Cursor < last.Sequence {
			return fmt.Errorf("event acknowledgment behind sent batch")
		}
		body, _ := json.Marshal(page.Cursor)
		if err := os.WriteFile(cursorPath+".tmp", body, 0600); err != nil {
			return err
		}
		if err := os.Rename(cursorPath+".tmp", cursorPath); err != nil {
			return err
		}
		cursor = page.Cursor
		batch.Events = nil
		bytes = 0
		return nil
	}
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}
		var e protocol.RunEvent
		if json.Unmarshal(line, &e) != nil {
			return errors.New("corrupt event spool")
		}
		if e.Sequence <= cursor {
			continue
		}
		if len(batch.Events) >= protocol.MaxRunEventBatch || bytes+len(line) > protocol.MaxRunEventBatchBytes/2 {
			if err = send(); err != nil {
				return err
			}
		}
		batch.Events = append(batch.Events, json.RawMessage(line))
		bytes += len(line)
	}
	return send()
}
func (w *Worker) streamRunEvents(ctx context.Context, spec protocol.RunSpec) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				call, cancel := context.WithTimeout(ctx, 5*time.Second)
				_ = w.flushRunEvents(call, spec)
				cancel()
			}
		}
	}()
	return func() { close(done); <-finished }
}
