package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// factoryLocalStore owns execution history. Nothing in these tables is published
// to GitHub. Issue snapshots are inputs, while specs and PRs remain remote links.
type factoryLocalStore struct {
	db        *sql.DB
	lock      *os.File
	seedHolds bool
}
type localFactoryTask struct {
	issue             GitHubIssue
	repository, alias string
}

func openFactoryLocalStore(path string) (*factoryLocalStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = os.Chmod(path, 0600); err != nil && !os.IsNotExist(err) {
		db.Close()
		return nil, err
	}
	_, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;
 CREATE TABLE IF NOT EXISTS factory_tasks (url TEXT PRIMARY KEY, issue TEXT NOT NULL, repository TEXT NOT NULL, alias TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS factory_events (id INTEGER PRIMARY KEY AUTOINCREMENT, task TEXT NOT NULL, body TEXT NOT NULL, created TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS factory_events_task ON factory_events(task,id);
 CREATE TABLE IF NOT EXISTS factory_receipts (run TEXT PRIMARY KEY, instance TEXT NOT NULL, token TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS job_requests (request_id TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, task TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS store_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS factory_prompts (repository TEXT NOT NULL, role TEXT NOT NULL, prompt TEXT NOT NULL, hash TEXT NOT NULL, PRIMARY KEY(repository,role));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0600)
	return &factoryLocalStore{db: db}, nil
}
func (s *factoryLocalStore) tasks(ctx context.Context) ([]localFactoryTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT issue,repository,alias FROM factory_tasks ORDER BY url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []localFactoryTask
	for rows.Next() {
		var raw string
		var task localFactoryTask
		if err = rows.Scan(&raw, &task.repository, &task.alias); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &task.issue); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}
func (s *factoryLocalStore) create(ctx context.Context, task localFactoryTask) error {
	return s.createRequest(ctx, task, "", "", "import")
}
func (s *factoryLocalStore) createRequest(ctx context.Context, task localFactoryTask, requestID, fingerprint, recipe string) error {
	raw, err := json.Marshal(task.issue)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO factory_tasks(url,issue,repository,alias) VALUES(?,?,?,?) ON CONFLICT(url) DO NOTHING`, task.issue.HTMLURL, string(raw), task.repository, task.alias)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 && recipe != "import" {
		role := "triage"
		if recipe == "direct" || recipe == "single" {
			role = "implement"
		}
		event, _ := json.Marshal(GitHubFactoryEvent{Version: 1, Kind: "created", Role: role, Recipe: recipe})
		if _, err = tx.ExecContext(ctx, `INSERT INTO factory_events(task,body,created) VALUES(?,?,?)`, task.issue.HTMLURL, GitHubFactoryMarker+string(event), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if requestID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO job_requests(request_id,fingerprint,task) VALUES(?,?,?)`, requestID, fingerprint, task.issue.HTMLURL); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *factoryLocalStore) comments(ctx context.Context, url string) ([]GitHubComment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,body,created FROM factory_events WHERE task=? ORDER BY id`, url)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []GitHubComment
	for rows.Next() {
		var c GitHubComment
		var date string
		if err = rows.Scan(&c.ID, &c.Body, &date); err != nil {
			return nil, err
		}
		c.User.Login = "local"
		c.CreatedAt, err = time.Parse(time.RFC3339Nano, date)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
func (s *factoryLocalStore) append(ctx context.Context, url string, event GitHubFactoryEvent, receipt *factoryReceipt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var latest int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM factory_events WHERE task=?`, url).Scan(&latest); err != nil {
		return err
	}
	if latest != event.Previous {
		return ErrWorkflowAction
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO factory_events(task,body,created) VALUES(?,?,?)`, url, GitHubFactoryMarker+string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if receipt != nil {
		if _, err = tx.ExecContext(ctx, `INSERT INTO factory_receipts(run,instance,token) VALUES(?,?,?)`, receipt.run, receipt.instance, factoryTokenHash(receipt.token)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type factoryReceipt struct{ run, instance, token string }

func (s *factoryLocalStore) receipt(ctx context.Context, run, instance, token string) bool {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM factory_receipts WHERE run=? AND instance=? AND token=?`, run, instance, factoryTokenHash(token)).Scan(&count)
	return err == nil && count == 1
}
func (s *factoryLocalStore) prompt(ctx context.Context, repo, role string) (string, string, error) {
	var prompt, hash string
	err := s.db.QueryRowContext(ctx, `SELECT prompt,hash FROM factory_prompts WHERE repository=? AND role=?`, repo, role).Scan(&prompt, &hash)
	return prompt, hash, err
}
func (s *factoryLocalStore) savePrompt(ctx context.Context, repo, role, prompt, expected, baseHash string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT hash FROM factory_prompts WHERE repository=? AND role=?`, repo, role).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		current = baseHash
	} else if err != nil {
		return "", err
	}
	if current != expected {
		return "", errAgentConflict
	}
	sum := sha256.Sum256([]byte(prompt))
	hash := hex.EncodeToString(sum[:])
	_, err = tx.ExecContext(ctx, `INSERT INTO factory_prompts(repository,role,prompt,hash) VALUES(?,?,?,?) ON CONFLICT(repository,role) DO UPDATE SET prompt=excluded.prompt,hash=excluded.hash`, repo, role, prompt, hash)
	if err != nil {
		return "", err
	}
	return hash, tx.Commit()
}

func factoryTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *factoryLocalStore) updateIssue(ctx context.Context, issue GitHubIssue) error {
	raw, err := json.Marshal(issue)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE factory_tasks SET issue=? WHERE url=?`, string(raw), issue.HTMLURL)
	return err
}

func (s *factoryLocalStore) request(ctx context.Context, id string) (string, string, error) {
	var fingerprint, task string
	err := s.db.QueryRowContext(ctx, `SELECT fingerprint,task FROM job_requests WHERE request_id=?`, id).Scan(&fingerprint, &task)
	return fingerprint, task, err
}
