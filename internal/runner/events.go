package runner

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const maxRecordedOutputBytes = 64 << 20

var errOutputLimit = errors.New("agent output exceeded the 64 MiB recording limit")

type Event struct {
	Sequence int64     `json:"sequence"`
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Stream   string    `json:"stream,omitempty"`
	Message  string    `json:"message,omitempty"`
	Encoding string    `json:"encoding,omitempty"`
	Data     string    `json:"data,omitempty"`
}

func (log *eventLog) appendOutput(stream string, data []byte) error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.outputBytes+int64(len(data)) > maxRecordedOutputBytes {
		return errOutputLimit
	}
	log.outputBytes += int64(len(data))
	log.sequence++
	if err := log.encoder.Encode(Event{
		Sequence: log.sequence,
		Time:     time.Now().UTC(),
		Type:     "process.output",
		Stream:   stream,
		Encoding: "base64",
		Data:     base64.StdEncoding.EncodeToString(data),
	}); err != nil {
		return fmt.Errorf("write event log: %w", err)
	}
	return nil
}

type eventLog struct {
	mu          sync.Mutex
	file        *os.File
	encoder     *json.Encoder
	sequence    int64
	outputBytes int64
}

func newEventLog(path string) (*eventLog, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create event log: %w", err)
	}
	return &eventLog{file: file, encoder: json.NewEncoder(file)}, nil
}

func (log *eventLog) append(eventType, stream, message string) error {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.sequence++
	if err := log.encoder.Encode(Event{
		Sequence: log.sequence,
		Time:     time.Now().UTC(),
		Type:     eventType,
		Stream:   stream,
		Message:  message,
	}); err != nil {
		return fmt.Errorf("write event log: %w", err)
	}
	return nil
}

func (log *eventLog) sync() error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if err := log.file.Sync(); err != nil {
		return fmt.Errorf("sync event log: %w", err)
	}
	return nil
}

func (log *eventLog) close() error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if err := log.file.Sync(); err != nil {
		_ = log.file.Close()
		return fmt.Errorf("sync event log: %w", err)
	}
	if err := log.file.Close(); err != nil {
		return fmt.Errorf("close event log: %w", err)
	}
	return nil
}
