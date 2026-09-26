// Package storage implements durable telemetry storage using SQLite.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cajui/cajui-central/internal/telemetry"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

// Open migrates the database transactionally. A single connection serializes writes.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err = s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	return s, nil
}
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck -- rollback after commit is harmless
	var version int
	if err = tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > 4 {
		return fmt.Errorf("unsupported schema version %d", version)
	}
	if version == 0 {
		_, err = tx.Exec(`CREATE TABLE readings (
   id INTEGER PRIMARY KEY, node_id TEXT NOT NULL, sensor_id TEXT NOT NULL,
   session_id TEXT NOT NULL, sequence INTEGER NOT NULL, metric TEXT NOT NULL,
   payload TEXT NOT NULL,
   UNIQUE(node_id,sensor_id,session_id,sequence,metric));
   PRAGMA user_version=1;`)
		if err != nil {
			return err
		}
	}
	if version < 2 {
		_, err = tx.Exec(`CREATE TABLE samples (
          id INTEGER PRIMARY KEY, source_id TEXT NOT NULL, device_id TEXT NOT NULL,
          sample_id TEXT NOT NULL, payload TEXT NOT NULL, received_at TEXT NOT NULL,
          UNIQUE(source_id,device_id,sample_id));
          CREATE INDEX samples_device ON samples(source_id,device_id,id);
          PRAGMA user_version=2;`)
		if err != nil {
			return err
		}
	}
	if version < 3 {
		if _, err = tx.Exec(workspaceSchema); err != nil {
			return err
		}
		if err = backfillWorkspace(tx); err != nil {
			return err
		}
	}
	if version < 4 {
		if _, err = tx.Exec(deviceStateSchema); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Insert treats exact retries as duplicates, but rejects identity reuse with changed data.
// receivedAt is always assigned by the receiving service, never by the sender.
func (s *Store) Insert(ctx context.Context, r telemetry.Reading, receivedAt time.Time) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	r.ReceivedAt = receivedAt.UTC()
	payload, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO readings(node_id,sensor_id,session_id,sequence,metric,payload)
 VALUES(?,?,?,?,?,?) ON CONFLICT(node_id,sensor_id,session_id,sequence,metric) DO NOTHING`, r.NodeID, r.SensorID, r.SessionID, r.Sequence, r.Metric, string(payload))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 1 {
		if err = observeReading(ctx, tx, r); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM readings WHERE node_id=? AND sensor_id=? AND session_id=? AND sequence=? AND metric=?`, r.NodeID, r.SensorID, r.SessionID, r.Sequence, r.Metric).Scan(&existing)
	if err != nil {
		return false, err
	}
	var old telemetry.Reading
	if err = json.Unmarshal([]byte(existing), &old); err != nil {
		return false, err
	}
	sameTime := old.MeasuredAt == nil && r.MeasuredAt == nil || old.MeasuredAt != nil && r.MeasuredAt != nil && old.MeasuredAt.Equal(*r.MeasuredAt)
	if old.Value != r.Value || old.Unit != r.Unit || !sameTime {
		return false, telemetry.ErrConflict
	}
	return false, nil
}

// Recent returns newest received records first, with bounded memory usage.
func (s *Store) Recent(ctx context.Context, limit int) ([]telemetry.Reading, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("limit must be between 1 and 100")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM readings ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	readings := make([]telemetry.Reading, 0)
	for rows.Next() {
		var payload string
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		var r telemetry.Reading
		if err = json.Unmarshal([]byte(payload), &r); err != nil {
			return nil, err
		}
		readings = append(readings, r)
	}
	return readings, rows.Err()
}
