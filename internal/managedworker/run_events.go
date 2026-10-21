package managedworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	type checkpoint struct {
		Cursor int64 `json:"cursor"`
		Offset int64 `json:"offset"`
	}
	var ack checkpoint
	if body, err := os.ReadFile(cursorPath); err == nil {
		if json.Unmarshal(body, &ack) != nil {
			if err = json.Unmarshal(body, &ack.Cursor); err != nil {
				return fmt.Errorf("invalid event acknowledgment: %w", err)
			}
		}
	}
	cursor, offset := ack.Cursor, ack.Offset
	if cursor < 0 || offset < 0 {
		return errors.New("invalid event acknowledgment")
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if offset > info.Size() {
		return errors.New("event spool shorter than acknowledged offset")
	}
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(file, 128<<10)
	readOffset, safeOffset, batchEnd := offset, offset, offset
	save := func(nextCursor, nextOffset int64) error {
		body, _ := json.Marshal(checkpoint{Cursor: nextCursor, Offset: nextOffset})
		if err := os.WriteFile(cursorPath+".tmp", body, 0600); err != nil {
			return err
		}
		if err := os.Rename(cursorPath+".tmp", cursorPath); err != nil {
			return err
		}
		cursor, offset = nextCursor, nextOffset
		return nil
	}
	batch := protocol.RunEventBatch{InstanceID: w.instanceID, LeaseToken: spec.LeaseToken}
	bytes := 0
	send := func() error {
		if len(batch.Events) == 0 {
			if safeOffset > offset {
				return save(cursor, safeOffset)
			}
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
		if err := save(page.Cursor, batchEnd); err != nil {
			return err
		}
		safeOffset = batchEnd
		batch.Events = nil
		bytes = 0
		return nil
	}
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			break
		}
		readOffset += int64(len(line))
		var e protocol.RunEvent
		if json.Unmarshal(line, &e) != nil {
			return errors.New("corrupt event spool")
		}
		if e.Sequence <= cursor {
			safeOffset = readOffset
			continue
		}
		if len(batch.Events) >= protocol.MaxRunEventBatch || bytes+len(line) > protocol.MaxRunEventBatchBytes/2 {
			if err = send(); err != nil {
				return err
			}
		}
		batch.Events = append(batch.Events, json.RawMessage(line))
		bytes += len(line)
		batchEnd = readOffset
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
