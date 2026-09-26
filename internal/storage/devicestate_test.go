package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cajui/cajui-central/internal/devicestate"
)

func stateFixture(t *testing.T, name, device string) devicestate.State {
	t.Helper()
	b, err := os.ReadFile("../../examples/mqtt/" + name)
	if err != nil {
		t.Fatal(err)
	}
	s, err := devicestate.Decode("demo-source", device, b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeviceStatesKeepTheLatestAndIgnoreRepeatedSnapshots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	receiver := stateFixture(t, "receiver-state.json", "00000000000000d1")
	transmitter := stateFixture(t, "transmitter-state.json", "00000000000000d2")
	start := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	if err = db.SaveAvailability(ctx, "demo-source", "00000000000000d1", "online", start, false); err != nil {
		t.Fatal(err) // Before any state: kept for when the state arrives.
	}
	if err = db.SaveDeviceState(ctx, transmitter, start, false); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveDeviceState(ctx, receiver, start, false); err != nil {
		t.Fatal(err)
	}
	// The broker repeats both retained messages on every subscription.
	later := start.Add(time.Hour)
	if err = db.SaveDeviceState(ctx, receiver, later, true); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveAvailability(ctx, "demo-source", "00000000000000d1", "online", later, true); err != nil {
		t.Fatal(err)
	}
	states, err := db.DeviceStates(ctx)
	if err != nil || len(states) != 2 {
		t.Fatal(states, err)
	}
	r := states[0]
	if r.Role != "receiver" || !r.ReceivedAt.Equal(start) || r.Retained || *r.Availability != "online" || !r.AvailabilityAt.Equal(start) {
		t.Fatalf("%+v", r)
	}
	if states[1].Role != "transmitter" || states[1].Availability != nil || states[1].AvailabilityAt != nil {
		t.Fatalf("%+v", states[1])
	}
	// A changed snapshot of unknown age replaces the state and is marked retained.
	uptime := int64(7200)
	receiver.UptimeS = &uptime
	if err = db.SaveDeviceState(ctx, receiver, later, true); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveAvailability(ctx, "demo-source", "00000000000000d1", "offline", later, false); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	states, err = db.DeviceStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r = states[0]
	if *r.UptimeS != 7200 || !r.Retained || !r.ReceivedAt.Equal(later) || *r.Availability != "offline" || !r.AvailabilityAt.Equal(later) {
		t.Fatalf("%+v", r)
	}
	// A live message clears the retained mark.
	if err = db.SaveDeviceState(ctx, receiver, later.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if states, _ = db.DeviceStates(ctx); states[0].Retained {
		t.Fatal("live state still marked retained")
	}
}

func TestDeviceStateStorageFailures(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	receiver := stateFixture(t, "receiver-state.json", "00000000000000d1")
	if err = db.SaveDeviceState(ctx, receiver, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`UPDATE device_states SET received_at='bad'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DeviceStates(ctx); err == nil {
		t.Fatal("bad receipt time accepted")
	}
	if _, err = db.db.Exec(`UPDATE device_states SET received_at='2026-09-26T12:00:00Z', state='{'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DeviceStates(ctx); err == nil {
		t.Fatal("bad state accepted")
	}
	if _, err = db.db.Exec(`UPDATE device_states SET state='{}'; INSERT INTO device_availability VALUES('demo-source','00000000000000d1','online','bad')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DeviceStates(ctx); err == nil {
		t.Fatal("bad availability time accepted")
	}
	db.Close()
	if err = db.SaveDeviceState(ctx, receiver, time.Now(), false); err == nil {
		t.Fatal("closed database accepted a state")
	}
	if _, err = db.DeviceStates(ctx); err == nil {
		t.Fatal("closed database listed states")
	}
}
