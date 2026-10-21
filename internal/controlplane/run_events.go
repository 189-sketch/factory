package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func (s *factoryLocalStore) ingestRunEvents(ctx context.Context, run string, events []json.RawMessage) (int64, error) {
	if len(events) > protocol.MaxRunEventBatch {
		return 0, errors.New("event batch too large")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var cursor int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM run_activity_events WHERE run=?`, run).Scan(&cursor); err != nil {
		return 0, err
	}
	for _, raw := range events {
		var event protocol.RunEvent
		if len(raw) > 128<<10 || json.Unmarshal(raw, &event) != nil || event.Sequence < 1 || event.Type == "" {
			return 0, errors.New("invalid run event")
		}
		if event.Sequence <= cursor {
			var previous string
			if err = tx.QueryRowContext(ctx, `SELECT body FROM run_activity_events WHERE run=? AND sequence=?`, run, event.Sequence).Scan(&previous); err != nil {
				return 0, err
			}
			var a, b any
			_ = json.Unmarshal([]byte(previous), &a)
			_ = json.Unmarshal(raw, &b)
			left, _ := json.Marshal(a)
			right, _ := json.Marshal(b)
			if string(left) != string(right) {
				return 0, errors.New("event sequence payload conflict")
			}
			continue
		}
		if event.Sequence != cursor+1 {
			return 0, fmt.Errorf("event sequence gap after %d", cursor)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO run_activity_events(run,sequence,body) VALUES(?,?,?)`, run, event.Sequence, string(raw)); err != nil {
			return 0, err
		}
		cursor = event.Sequence
	}
	return cursor, tx.Commit()
}
func (s *GitHubFactoryServer) ingestEvents(w http.ResponseWriter, r *http.Request) {
	if !limitRequestBody(w, r, protocol.MaxRunEventBatchBytes) {
		return
	}
	var p protocol.RunEventBatch
	if err := decodeJSON(r, &p); err != nil {
		writeDecodeError(w, err)
		return
	}
	// Hold the lease lock through ingestion so expiry cannot race authorization.
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	lease := s.leases[r.PathValue("id")]
	cleanup := false
	if lease == nil {
		lease = s.cleanupLeases[r.PathValue("id")]
		cleanup = lease != nil
	}
	validTime := lease != nil && ((!cleanup && time.Since(lease.last) <= leaseDuration) || (cleanup && time.Now().Before(lease.cleanupUntil)))
	if !validTime || lease.instance != p.InstanceID || lease.spec.LeaseToken != p.LeaseToken {
		writeError(w, 409, ErrLeaseConflict)
		return
	}
	cursor, err := s.local.ingestRunEvents(r.Context(), r.PathValue("id"), p.Events)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 200, protocol.RunEventPage{Cursor: cursor, Events: []json.RawMessage{}})
}
func (s *GitHubFactoryServer) runEvents(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if after < 0 {
		writeError(w, 400, errors.New("invalid cursor"))
		return
	}
	rows, err := s.local.db.QueryContext(r.Context(), `SELECT sequence,body FROM run_activity_events WHERE run=? AND sequence>? ORDER BY sequence LIMIT 128`, r.PathValue("id"), after)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer rows.Close()
	page := protocol.RunEventPage{Cursor: after, Events: []json.RawMessage{}}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&page.Cursor, &raw); err != nil {
			writeError(w, 500, err)
			return
		}
		page.Events = append(page.Events, json.RawMessage(raw))
	}
	if err = rows.Err(); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, page)
}
