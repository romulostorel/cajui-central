package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cajui/cajui-central/internal/devicestate"
)

type repo struct {
	state             devicestate.State
	stateErr, saveErr error
	deleteErr         error
	records           map[string]devicestate.CommandRecord
	deleted           []string
}

func (r *repo) DeviceState(context.Context, string, string) (devicestate.State, error) {
	return r.state, r.stateErr
}
func (r *repo) InsertCommand(_ context.Context, source, device string, c devicestate.Command, at time.Time) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.records[c.ID] = devicestate.CommandRecord{ID: c.ID, SourceID: source, DeviceID: device, Type: c.Type, NodeID: c.NodeID, Status: "sent", SentAt: at, UpdatedAt: at}
	return nil
}
func (r *repo) DeleteCommand(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	delete(r.records, id)
	return r.deleteErr
}
func (r *repo) Command(_ context.Context, id string) (devicestate.CommandRecord, error) {
	record, ok := r.records[id]
	if !ok {
		return record, devicestate.ErrUnknownCommand
	}
	return record, nil
}

type publisher struct {
	err  error
	sent []devicestate.Command
}

func (p *publisher) PublishCommand(_ context.Context, _, _ string, c devicestate.Command) error {
	p.sent = append(p.sent, c)
	return p.err
}

func newRepo() *repo {
	return &repo{state: devicestate.State{Role: "receiver", Capabilities: []string{"pairing", "revoke"}}, records: map[string]devicestate.CommandRecord{}}
}

func TestSendRecordsBeforePublishingAndReportsStatus(t *testing.T) {
	ctx := context.Background()
	r, p := newRepo(), &publisher{}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	record, err := Send(ctx, r, p, "r", "00000000000000d1", "pairing.accept", "00000000000000a2", now)
	if err != nil || record.Status != "sent" || len(p.sent) != 1 || p.sent[0].ID != record.ID || p.sent[0].NodeID != "00000000000000a2" {
		t.Fatal(record, err, p.sent)
	}
	if got, err := Status(ctx, r, record.ID, now.Add(devicestate.CommandTimeout-time.Second)); err != nil || got.Status != "sent" {
		t.Fatal(got, err)
	}
	if got, _ := Status(ctx, r, record.ID, now.Add(devicestate.CommandTimeout)); got.Status != "undelivered" {
		t.Fatal(got)
	}
	answered := r.records[record.ID]
	answered.Status = "applied"
	r.records[record.ID] = answered
	if got, _ := Status(ctx, r, record.ID, now.Add(time.Hour)); got.Status != "applied" {
		t.Fatal(got)
	}
	if _, err := Status(ctx, r, "missing", now); !errors.Is(err, devicestate.ErrUnknownCommand) {
		t.Fatal(err)
	}
}

func TestSendFailures(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	if _, err := Send(ctx, newRepo(), nil, "r", "d", "pairing.open", "", now); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	r := newRepo()
	r.stateErr = devicestate.ErrUnknownDevice
	if _, err := Send(ctx, r, &publisher{}, "r", "d", "pairing.open", "", now); !errors.Is(err, devicestate.ErrUnknownDevice) {
		t.Fatal(err)
	}
	if _, err := Send(ctx, newRepo(), &publisher{}, "r", "d", "firmware.install", "", now); !errors.Is(err, devicestate.ErrInvalid) {
		t.Fatal(err)
	}
	r = newRepo()
	r.saveErr = errors.New("disk full")
	p := &publisher{}
	if _, err := Send(ctx, r, p, "r", "d", "pairing.open", "", now); err == nil || len(p.sent) != 0 {
		t.Fatal("published a command that was not recorded")
	}
	// A command that could not be published leaves no record behind.
	r, p = newRepo(), &publisher{err: ErrUnavailable}
	if _, err := Send(ctx, r, p, "r", "d", "pairing.open", "", now); !errors.Is(err, ErrUnavailable) || len(r.records) != 0 || len(r.deleted) != 1 {
		t.Fatal(err, r.records)
	}
	r.deleteErr = errors.New("locked")
	if _, err := Send(ctx, r, p, "r", "d", "pairing.open", "", now); !errors.Is(err, ErrUnavailable) || !errors.Is(err, r.deleteErr) {
		t.Fatal(err)
	}
}
