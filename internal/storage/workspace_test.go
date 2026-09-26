package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cajui/cajui-central/internal/telemetry"
	"github.com/cajui/cajui-central/internal/workspace"
)

func catalog(t *testing.T, s *Store) workspace.Catalog {
	t.Helper()
	c, err := s.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestWorkspaceInventoryNamesAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Now().UTC()
	r := sample()
	if _, err = s.Insert(ctx, r, at); err != nil {
		t.Fatal(err)
	}
	initial := catalog(t, s)
	if len(initial.Devices) != 1 || len(initial.Sensors) != 1 || initial.Devices[0].Name != "" {
		t.Fatal(initial)
	}
	d, sen := initial.Devices[0], initial.Sensors[0]
	if err = s.SaveSensor(ctx, sen.ID, workspace.Settings{Name: "Ambient"}); !errors.Is(err, workspace.ErrUnregistered) {
		t.Fatal(err)
	}
	if err = s.SaveDevice(ctx, d.ID, workspace.Settings{Name: "North", Location: "Field"}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveDevice(ctx, d.ID, workspace.Settings{Name: "Lost update"}); !errors.Is(err, workspace.ErrConflict) {
		t.Fatal(err)
	}
	if err = s.SaveSensor(ctx, sen.ID, workspace.Settings{Name: "Ambient"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Insert(ctx, r, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	c := catalog(t, s)
	if c.Devices[0].ReceivedAt != d.ReceivedAt || c.Devices[0].Name != "North" || c.Sensors[0].Name != "Ambient" {
		t.Fatal(c)
	}
	// Move the sensor outside the last 100 records. Inventory and last value persist.
	r.SensorID = "other"
	for i := 0; i < 105; i++ {
		r.Sequence = int64(i + 2)
		if _, err = s.Insert(ctx, r, at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	c = catalog(t, s)
	if len(c.Sensors) != 2 || c.Sensors[0].Measurements[0].Value == nil {
		t.Fatal(c)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !reflect.DeepEqual(c, catalog(t, s)) {
		t.Fatal("reopen changed catalog")
	}
	if err = s.SaveSensor(ctx, sen.ID, workspace.Settings{Name: "Renamed", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM readings`).Scan(&count); err != nil || count != 106 {
		t.Fatal(count, err)
	}
}
func TestWorkspaceScopesLatestStatusAndAtomicity(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	at := time.Now().UTC()
	msg := sampleFixture(t)
	msg.DeviceID = "node"
	msg.SourceID = "HTTP"
	msg.Readings = []telemetry.Measurement{{SensorID: "sensor", Metric: "temperature", Unit: "degC", Status: "error"}}
	for i, source := range []string{"HTTP", "another"} {
		msg.SourceID = source
		msg.SampleID = fmt.Sprint(i)
		if _, err := s.InsertSample(ctx, msg, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Insert(ctx, sample(), at); err != nil {
		t.Fatal(err)
	}
	c := catalog(t, s)
	if len(c.Devices) != 3 || len(c.Sensors) != 3 {
		t.Fatal(c)
	}
	if c.Sensors[0].Measurements[0].Value != nil || c.Sensors[0].Measurements[0].Status != "error" {
		t.Fatal(c)
	}
	before := c
	if _, err := s.InsertSample(ctx, msg, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, catalog(t, s)) {
		t.Fatal("retry refreshed inventory")
	}
	// An inventory failure must roll back the entire new sample.
	if _, err := s.db.Exec(`CREATE TRIGGER reject_observation BEFORE INSERT ON workspace_measurements BEGIN SELECT RAISE(ABORT,'injected failure'); END;`); err != nil {
		t.Fatal(err)
	}
	msg.SampleID = "fail"
	if _, err := s.InsertSample(ctx, msg, at.Add(time.Hour)); err == nil {
		t.Fatal("failed inventory committed")
	}
	r := sample()
	r.Sequence++
	if _, err := s.Insert(ctx, r, at.Add(time.Hour)); err == nil {
		t.Fatal("failed HTTP inventory committed")
	}
	if !reflect.DeepEqual(before, catalog(t, s)) {
		t.Fatal("partial inventory write")
	}
	samples, err := s.RecentSamples(ctx, 100)
	if err != nil || len(samples) != 2 {
		t.Fatal(samples, err)
	}
}
func TestWorkspaceLayoutValidationAndConflict(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.Insert(ctx, sample(), time.Now()); err != nil {
		t.Fatal(err)
	}
	c := catalog(t, s)
	d, sen := c.Devices[0].ID, c.Sensors[0].ID
	deviceItem := workspace.Item{Kind: "device", DeviceID: d}
	sensorItem := workspace.Item{Kind: "sensor", SensorID: sen}
	metricItem := workspace.Item{Kind: "measurement", SensorID: sen, Metric: "temperature", Unit: "degC"}
	layout := workspace.Layout{Sections: []workspace.Section{{Title: "North", Items: []workspace.Item{deviceItem, sensorItem, metricItem}}}}
	if err := s.SaveLayout(ctx, layout); !errors.Is(err, workspace.ErrUnregistered) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id       int64
		settings workspace.Settings
		want     error
	}{{-1, workspace.Settings{Name: "x"}, workspace.ErrInvalid}, {d, workspace.Settings{}, workspace.ErrInvalid}, {999, workspace.Settings{Name: "x"}, workspace.ErrNotFound}} {
		if err := s.SaveDevice(ctx, tc.id, tc.settings); !errors.Is(err, tc.want) {
			t.Fatal(err)
		}
	}
	if err := s.SaveDevice(ctx, d, workspace.Settings{Name: "Device"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLayout(ctx, layout); !errors.Is(err, workspace.ErrUnregistered) {
		t.Fatal(err)
	}
	if err := s.SaveSensor(ctx, sen, workspace.Settings{Name: "Sensor"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []workspace.Item{{Kind: "device", DeviceID: 999}, {Kind: "sensor", SensorID: 999}, {Kind: "measurement", SensorID: sen, Metric: "missing", Unit: "degC"}} {
		if err := s.SaveLayout(ctx, workspace.Layout{Sections: []workspace.Section{{Title: "Bad", Items: []workspace.Item{bad}}}}); !errors.Is(err, workspace.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if err := s.SaveLayout(ctx, layout); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLayout(ctx, layout); !errors.Is(err, workspace.ErrConflict) {
		t.Fatal(err)
	}
	c = catalog(t, s)
	if c.Layout.Revision != 1 || !reflect.DeepEqual(c.Layout.Sections, layout.Sections) {
		t.Fatal(c.Layout)
	}
	if err := s.SaveLayout(ctx, workspace.Layout{Revision: 1, Sections: []workspace.Section{}}); err != nil {
		t.Fatal(err)
	}
	c = catalog(t, s)
	if c.Layout.Sections == nil || c.Devices[0].Name == "" || len(c.Sensors) != 1 {
		t.Fatal("removing dashboard items changed registration")
	}
	if err := s.SaveLayout(ctx, workspace.Layout{Revision: 2}); err != nil {
		t.Fatal(err)
	}
	if catalog(t, s).Layout.Sections != nil {
		t.Fatal("automatic layout not restored")
	}
	if err := s.SaveLayout(ctx, workspace.Layout{Revision: -1}); !errors.Is(err, workspace.ErrInvalid) {
		t.Fatal(err)
	}
}
func TestWorkspaceDiagnosticsAndFailure(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	r := sample()
	r.SensorID = "radio"
	r.Metric = "rssi"
	r.Unit = "dBm"
	if _, err := s.Insert(ctx, r, time.Now()); err != nil {
		t.Fatal(err)
	}
	c := catalog(t, s)
	d, sen := c.Devices[0].ID, c.Sensors[0].ID
	if err := s.SaveDevice(ctx, d, workspace.Settings{Name: "D"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSensor(ctx, sen, workspace.Settings{Name: "Radio"}); !errors.Is(err, workspace.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.SaveLayout(ctx, workspace.Layout{Sections: []workspace.Section{{Title: "X", Items: []workspace.Item{{Kind: "measurement", SensorID: sen, Metric: "rssi", Unit: "dBm"}}}}}); !errors.Is(err, workspace.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Catalog(ctx); err == nil {
		t.Fatal("closed database read")
	}
	if err := s.SaveDevice(ctx, d, workspace.Settings{Name: "X"}); err == nil {
		t.Fatal("closed database write")
	}
	if err := s.SaveLayout(ctx, workspace.Layout{}); err == nil {
		t.Fatal("closed database write")
	}
}
func TestWorkspaceMigrationBackfillsAllHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at := time.Now().UTC()
	if _, err = s.Insert(ctx, sample(), at); err != nil {
		t.Fatal(err)
	}
	msg := sampleFixture(t)
	if _, err = s.InsertSample(ctx, msg, at); err != nil {
		t.Fatal(err)
	}
	before := catalog(t, s)
	if _, err = s.db.Exec(`DROP TABLE workspace_measurements; DROP TABLE workspace_sensors; DROP TABLE workspace_devices; DROP TABLE workspace_layout; DROP TABLE device_states; DROP TABLE device_availability; PRAGMA user_version=2;`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !reflect.DeepEqual(before, catalog(t, s)) {
		t.Fatal("migration changed discovered identity, values or receipt time")
	}
}

func TestWorkspaceMigrationFailureRollsBack(t *testing.T) {
	s := openTest(t)
	if _, err := s.Insert(context.Background(), sample(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE workspace_measurements; DROP TABLE workspace_sensors; DROP TABLE workspace_devices; DROP TABLE workspace_layout; UPDATE readings SET payload='broken'; PRAGMA user_version=2;`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err == nil {
		t.Fatal("corrupt telemetry migrated")
	}
	var version, count int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 2 {
		t.Fatal(version, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='workspace_devices'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial migration", count, err)
	}
}
