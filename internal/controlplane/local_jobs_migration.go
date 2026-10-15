package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// A process lock protects the single-owner local store, including cutover. The
// v1 archive and worker output directories remain private and are never deleted.
func openLocalJobs(directory string) (*factoryLocalStore, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(directory, "jobs.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("another control plane owns the local jobs store: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			lock.Close()
		}
	}()
	path := filepath.Join(directory, "jobs.db")
	legacy := filepath.Join(directory, "factory.db")
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, err = os.Stat(legacy); err == nil {
			// SQLite's backup snapshot includes committed WAL data. A partial copy is
			// discarded on retry, never opened as the active store.
			temporary := path + ".migrating"
			if err = os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			db, openErr := sql.Open("sqlite", legacy)
			if openErr != nil {
				return nil, openErr
			}
			_, err = db.Exec("VACUUM INTO '" + strings.ReplaceAll(temporary, "'", "''") + "'")
			closeErr := db.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if err = os.Chmod(temporary, 0600); err != nil {
				return nil, err
			}
			if err = os.Rename(temporary, path); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	store, err := openFactoryLocalStore(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			store.db.Close()
		}
	}()
	if _, err = os.Stat(legacy); err == nil {
		archive := filepath.Join(directory, "archive")
		if err = os.MkdirAll(archive, 0700); err != nil {
			return nil, err
		}
		destination := filepath.Join(archive, "factory-v1.db")
		if _, err = os.Stat(destination); err == nil {
			return nil, errors.New("both active and archived v1 databases exist; inspect them before cutover")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err = os.Rename(legacy, destination); err != nil {
			return nil, err
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if err = os.Rename(legacy+suffix, destination+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	// The marker is committed only after every existing blocked job has a hold.
	// A crash while seeding resumes safely on the next startup.
	var initialized int
	if err = store.db.QueryRow(`SELECT count(*) FROM store_metadata WHERE key='local_jobs_v1'`).Scan(&initialized); err != nil {
		return nil, err
	}
	store.seedHolds = initialized == 0
	store.lock = lock
	manifest, _ := json.MarshalIndent(map[string]any{"version": 1, "active": "jobs.db", "factory_archive": "archive/factory-v1.db", "legacy_direct_store": "unchanged; legacy server mode remains separate", "worker_outputs": "onboarding/worker retained in place; included in runtime backup", "authority": "one jobs.db; no dual writes"}, "", "  ")
	if err = os.WriteFile(filepath.Join(directory, "jobs-migration.json"), manifest, 0600); err != nil {
		return nil, err
	}
	ok = true
	return store, nil
}

func (s *GitHubFactoryServer) seedPublicationHolds(ctx context.Context) error {
	if !s.local.seedHolds {
		return nil
	}
	for _, task := range s.tasks {
		if (task.state.Job.State == "blocked" || task.state.Job.ID == "github_c64108094ef7986624f5c339") && task.state.Job.PublicationHold == "" {
			if err := s.event(ctx, task, GitHubFactoryEvent{Kind: "publication_hold", Feedback: "Imported blocked work: publication requires explicit operator review"}); err != nil {
				return err
			}
		}
	}
	if _, err := s.local.db.ExecContext(ctx, `INSERT OR IGNORE INTO store_metadata(key,value) VALUES('local_jobs_v1','complete')`); err != nil {
		return err
	}
	s.local.seedHolds = false
	return nil
}
