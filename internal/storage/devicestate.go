package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/cajui/cajui-central/internal/devicestate"
)

// Latest state and availability per device; history is not kept. Availability has its
// own table because a receiver announces "online" before its first state.
const deviceStateSchema = `
CREATE TABLE device_states (
 source_id TEXT NOT NULL, device_id TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('receiver','transmitter')),
 state TEXT NOT NULL, received_at TEXT NOT NULL, retained INTEGER NOT NULL CHECK(retained IN (0,1)),
 PRIMARY KEY(source_id,device_id));
CREATE TABLE device_availability (
 source_id TEXT NOT NULL, device_id TEXT NOT NULL,
 availability TEXT NOT NULL CHECK(availability IN ('online','offline')), received_at TEXT NOT NULL,
 PRIMARY KEY(source_id,device_id));
PRAGMA user_version=4;`

// SaveDeviceState keeps the latest state. A retained snapshot identical to the stored
// state is the broker repeating it on subscription, not news: its receipt time stays.
func (s *Store) SaveDeviceState(ctx context.Context, state devicestate.State, at time.Time, retained bool) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO device_states(source_id,device_id,role,state,received_at,retained) VALUES(?,?,?,?,?,?)
 ON CONFLICT(source_id,device_id) DO UPDATE SET role=excluded.role,state=excluded.state,received_at=excluded.received_at,retained=excluded.retained
 WHERE NOT (excluded.retained=1 AND device_states.state=excluded.state)`,
		state.SourceID, state.DeviceID, state.Role, string(payload), at.UTC().Format(time.RFC3339Nano), retained)
	return err
}

// SaveAvailability records a receiver's online/offline, with the same rule for
// repeated retained snapshots.
func (s *Store) SaveAvailability(ctx context.Context, source, device, value string, at time.Time, retained bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO device_availability(source_id,device_id,availability,received_at) VALUES(?,?,?,?)
 ON CONFLICT(source_id,device_id) DO UPDATE SET availability=excluded.availability,received_at=excluded.received_at
 WHERE NOT (? AND device_availability.availability=excluded.availability)`,
		source, device, value, at.UTC().Format(time.RFC3339Nano), retained)
	return err
}

// DeviceStates returns every known device state, receivers first.
func (s *Store) DeviceStates(ctx context.Context) ([]devicestate.Stored, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.state,s.received_at,s.retained,a.availability,a.received_at
 FROM device_states s LEFT JOIN device_availability a USING(source_id,device_id)
 ORDER BY s.role='transmitter',s.source_id,s.device_id LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []devicestate.Stored{}
	for rows.Next() {
		var payload, at string
		var availability, availabilityAt sql.NullString
		var item devicestate.Stored
		if err = rows.Scan(&payload, &at, &item.Retained, &availability, &availabilityAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(payload), &item.State); err != nil {
			return nil, err
		}
		if item.ReceivedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		if availability.Valid {
			item.Availability = &availability.String
		}
		if availabilityAt.Valid {
			t, err := time.Parse(time.RFC3339Nano, availabilityAt.String)
			if err != nil {
				return nil, err
			}
			item.AvailabilityAt = &t
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
