package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cajui/cajui-central/internal/devicestate"
)

func TestCommandsRecordOnlyAnswersToCentral(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	command := devicestate.Command{ID: "central-1", Type: "pairing.accept", NodeID: "00000000000000a2"}
	if err = db.InsertCommand(ctx, "r", "00000000000000d1", command, at); err != nil {
		t.Fatal(err)
	}
	got, err := db.Command(ctx, "central-1")
	if err != nil || got.Status != "sent" || got.Reason != nil || got.NodeID != "00000000000000a2" || !got.SentAt.Equal(at) {
		t.Fatal(got, err)
	}
	pending := devicestate.Result{CommandID: "central-1", Status: "pending"}
	// Another device answering the same ID is ignored.
	if err = db.SaveCommandResult(ctx, "r", "00000000000000ff", pending, at); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.Command(ctx, "central-1"); got.Status != "sent" {
		t.Fatal(got)
	}
	if err = db.SaveCommandResult(ctx, "r", "00000000000000d1", pending, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	closed := "closed"
	if err = db.SaveCommandResult(ctx, "r", "00000000000000d1", devicestate.Result{CommandID: "central-1", Status: "rejected", Reason: &closed}, at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// A final answer is never replaced.
	if err = db.SaveCommandResult(ctx, "r", "00000000000000d1", devicestate.Result{CommandID: "central-1", Status: "applied"}, at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err = db.Command(ctx, "central-1")
	if err != nil || got.Status != "rejected" || *got.Reason != "closed" || !got.UpdatedAt.Equal(at.Add(2*time.Second)) {
		t.Fatal(got, err)
	}
	if _, err = db.Command(ctx, "missing"); !errors.Is(err, devicestate.ErrUnknownCommand) {
		t.Fatal(err)
	}
	if err = db.DeleteCommand(ctx, "central-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Command(ctx, "central-1"); !errors.Is(err, devicestate.ErrUnknownCommand) {
		t.Fatal(err)
	}
	if err = db.InsertCommand(ctx, "r", "d", devicestate.Command{ID: "bad-time", Type: "pairing.open"}, at); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`UPDATE device_commands SET updated_at='bad'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Command(ctx, "bad-time"); err == nil {
		t.Fatal("bad time accepted")
	}
	if _, err = db.db.Exec(`UPDATE device_commands SET sent_at='bad'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Command(ctx, "bad-time"); err == nil {
		t.Fatal("bad time accepted")
	}
	db.Close()
	if _, err = db.Command(ctx, "x"); err == nil {
		t.Fatal("closed database read a command")
	}
}

func TestDeviceStateLookup(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.DeviceState(ctx, "demo-source", "00000000000000d1"); !errors.Is(err, devicestate.ErrUnknownDevice) {
		t.Fatal(err)
	}
	receiver := stateFixture(t, "receiver-state.json", "00000000000000d1")
	if err = db.SaveDeviceState(ctx, receiver, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	got, err := db.DeviceState(ctx, "demo-source", "00000000000000d1")
	if err != nil || got.Role != "receiver" {
		t.Fatal(got, err)
	}
	db.Close()
	if _, err = db.DeviceState(ctx, "demo-source", "00000000000000d1"); err == nil {
		t.Fatal("closed database read a state")
	}
}
