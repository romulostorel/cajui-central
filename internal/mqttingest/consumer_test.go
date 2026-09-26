package mqttingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/cajui/cajui-central/internal/commands"
	"github.com/cajui/cajui-central/internal/devicestate"
	"github.com/cajui/cajui-central/internal/telemetry"
)

type repository struct {
	calls, states, availability, deleted, results int
	retained                                      bool
	err                                           error
}

func (r *repository) SaveDeviceState(_ context.Context, _ devicestate.State, _ time.Time, retained bool) error {
	r.states++
	r.retained = retained
	return r.err
}
func (r *repository) SaveCommandResult(context.Context, string, string, devicestate.Result, time.Time) error {
	r.results++
	return r.err
}
func (r *repository) DeleteDeviceState(context.Context, string, string) error {
	r.deleted++
	return r.err
}
func (r *repository) DeleteAvailability(context.Context, string, string) error {
	r.deleted++
	return r.err
}
func (r *repository) SaveAvailability(_ context.Context, _, _, _ string, _ time.Time, retained bool) error {
	r.availability++
	r.retained = retained
	return r.err
}

func (r *repository) InsertSample(context.Context, telemetry.Sample, time.Time) (bool, error) {
	r.calls++
	return true, r.err
}
func TestHandle(t *testing.T) {
	b, e := os.ReadFile("../../examples/mqtt/sample.json")
	if e != nil {
		t.Fatal(e)
	}
	repo := &repository{}
	c := New(Config{}, repo, nil)
	topic := "telemetry/v1/demo-source/demo-device/samples"
	if ok, e := c.Handle(context.Background(), topic, b, false); !ok || e != nil || repo.calls != 1 {
		t.Fatal(ok, e, repo.calls)
	}
	if ok, e := c.Handle(context.Background(), topic, b, true); ok || e != nil || repo.calls != 1 {
		t.Fatal("retained snapshot accepted")
	}
	for _, tc := range []struct {
		topic string
		data  []byte
	}{{topic, []byte("bad")}, {"telemetry/v1/other/demo-device/samples", b}} {
		if _, e := c.Handle(context.Background(), tc.topic, tc.data, false); !errors.Is(e, telemetry.ErrInvalid) {
			t.Fatal(e)
		}
	}
	repo.err = errors.New("storage failed")
	if _, e := c.Handle(context.Background(), topic, b, false); e == nil {
		t.Fatal("swallowed storage failure")
	}
}
func TestHandleDeviceStateAndAvailability(t *testing.T) {
	b, e := os.ReadFile("../../examples/mqtt/receiver-state.json")
	if e != nil {
		t.Fatal(e)
	}
	repo := &repository{}
	c := New(Config{}, repo, nil)
	state := devicestate.Topic("demo-source", "00000000000000d1", "state")
	availability := devicestate.Topic("demo-source", "00000000000000d1", "availability")
	// Retained by design: accepted, and the flag reaches storage.
	if ok, e := c.Handle(context.Background(), state, b, true); !ok || e != nil || repo.states != 1 || !repo.retained {
		t.Fatal(ok, e, repo)
	}
	if ok, e := c.Handle(context.Background(), availability, []byte("online"), false); !ok || e != nil || repo.availability != 1 || repo.retained {
		t.Fatal(ok, e, repo)
	}
	for _, tc := range []struct {
		topic string
		data  []byte
	}{
		{state, []byte("{}")},
		{devicestate.Topic("other", "00000000000000d1", "state"), b},
		{availability, []byte("maybe")},
	} {
		if _, e := c.Handle(context.Background(), tc.topic, tc.data, false); !errors.Is(e, telemetry.ErrInvalid) {
			t.Fatal(tc.topic, e)
		}
	}
	if repo.calls != 0 || repo.states != 1 || repo.availability != 1 {
		t.Fatal(repo)
	}
	// Clearing a retained topic removes the device; the message is settled.
	for _, topic := range []string{state, availability} {
		if ok, e := c.Handle(context.Background(), topic, nil, false); !ok || e != nil {
			t.Fatal(topic, ok, e)
		}
	}
	if repo.deleted != 2 || repo.states != 1 || repo.availability != 1 {
		t.Fatal(repo)
	}
	results := devicestate.Topic("demo-source", "00000000000000d1", "results")
	answer := []byte(`{"version":1,"command_id":"central-1","status":"applied","reason":null}`)
	if ok, e := c.Handle(context.Background(), results, answer, false); !ok || e != nil || repo.results != 1 {
		t.Fatal(ok, e, repo)
	}
	if ok, e := c.Handle(context.Background(), results, answer, true); ok || e != nil || repo.results != 1 {
		t.Fatal("retained result accepted")
	}
	if _, e := c.Handle(context.Background(), results, []byte("{}"), false); !errors.Is(e, telemetry.ErrInvalid) {
		t.Fatal(e)
	}
	repo.err = errors.New("storage failed")
	if _, e := c.Handle(context.Background(), state, b, false); e == nil || settled(e) {
		t.Fatal("swallowed storage failure")
	}
}
func TestPublishCommandWithoutConnection(t *testing.T) {
	c := New(Config{}, &repository{}, nil)
	if err := c.PublishCommand(context.Background(), "s", "00000000000000d1", devicestate.Command{ID: "x", Type: "pairing.open"}); !errors.Is(err, commands.ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestRunCanceledAndUnavailable(t *testing.T) {
	c := New(Config{URL: "tcp://127.0.0.1:1", ClientID: "test"}, &repository{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c.Run(ctx)
	if c.Connected() {
		t.Fatal("connected without broker")
	}
	if pause(ctx, time.Hour) {
		t.Fatal("ignored cancellation")
	}
	if !pause(context.Background(), time.Millisecond) {
		t.Fatal("timer canceled")
	}
}

func TestSettledAcknowledgesStoredOrPermanentlyRejected(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, true},
		{telemetry.ErrInvalid, true},
		{telemetry.ErrConflict, true},
		{errors.New("database locked"), false},
		{context.DeadlineExceeded, false},
	} {
		if got := settled(tc.err); got != tc.want {
			t.Errorf("settled(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	inbox := make(chan message, 2)
	inbox <- message{}
	inbox <- message{}
	drain(inbox)
	if len(inbox) != 0 {
		t.Fatal("drain left messages")
	}
}
