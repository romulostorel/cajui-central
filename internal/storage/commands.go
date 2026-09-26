package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/cajui/cajui-central/internal/devicestate"
)

// Commands Central sent and their latest answers.
const commandSchema = `
CREATE TABLE device_commands (
 command_id TEXT PRIMARY KEY, source_id TEXT NOT NULL, device_id TEXT NOT NULL,
 type TEXT NOT NULL, node_id TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('sent','pending','applied','rejected')), reason TEXT,
 sent_at TEXT NOT NULL, updated_at TEXT NOT NULL);
PRAGMA user_version=5;`

func (s *Store) InsertCommand(ctx context.Context, source, device string, c devicestate.Command, at time.Time) error {
	stamp := at.UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO device_commands(command_id,source_id,device_id,type,node_id,status,sent_at,updated_at) VALUES(?,?,?,?,?,'sent',?,?)`,
		c.ID, source, device, c.Type, c.NodeID, stamp, stamp)
	return err
}

// SaveCommandResult records an answer to a command Central sent to that device; answers
// to anyone else's commands are ignored. A final answer is never replaced.
func (s *Store) SaveCommandResult(ctx context.Context, source, device string, r devicestate.Result, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE device_commands SET status=?,reason=?,updated_at=?
 WHERE command_id=? AND source_id=? AND device_id=? AND status IN ('sent','pending')`,
		r.Status, r.Reason, at.UTC().Format(time.RFC3339Nano), r.CommandID, source, device)
	return err
}

func (s *Store) Command(ctx context.Context, id string) (devicestate.CommandRecord, error) {
	var c devicestate.CommandRecord
	var reason sql.NullString
	var sent, updated string
	err := s.db.QueryRowContext(ctx, `SELECT command_id,source_id,device_id,type,node_id,status,reason,sent_at,updated_at FROM device_commands WHERE command_id=?`, id).
		Scan(&c.ID, &c.SourceID, &c.DeviceID, &c.Type, &c.NodeID, &c.Status, &reason, &sent, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return c, devicestate.ErrUnknownCommand
	}
	if err != nil {
		return c, err
	}
	if reason.Valid {
		c.Reason = &reason.String
	}
	if c.SentAt, err = time.Parse(time.RFC3339Nano, sent); err != nil {
		return c, err
	}
	c.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return c, err
}

// DeleteCommand removes a command that could not be published.
func (s *Store) DeleteCommand(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM device_commands WHERE command_id=?`, id)
	return err
}
