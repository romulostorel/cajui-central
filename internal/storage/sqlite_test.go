package storage

import (
	"context"
	"errors"
	"github.com/cajui/cajui-central/internal/telemetry"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func sample() telemetry.Reading {
	return telemetry.Reading{NodeID: "node", SensorID: "sensor", SessionID: "boot", Sequence: 1, Metric: "temperature", Value: 26.7, Unit: "degC"}
}
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func TestPersistenceAndIdempotency(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	r := sample()
	if ok, err := s.Insert(ctx, r, now); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := s.Insert(ctx, r, now.Add(time.Hour)); ok || err != nil {
		t.Fatal(ok, err)
	}
	r.Value++
	if _, err := s.Insert(ctx, r, now); !errors.Is(err, telemetry.ErrConflict) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.Recent(ctx, 100)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if !list[0].ReceivedAt.Equal(now) || list[0].Value != 26.7 {
		t.Fatal(list)
	}
	r = sample()
	r.SessionID = "reboot"
	if ok, err := s.Insert(ctx, r, now); !ok || err != nil {
		t.Fatal(ok, err)
	}
	r.Metric = "humidity"
	r.Unit = "%"
	if ok, err := s.Insert(ctx, r, now); !ok || err != nil {
		t.Fatal(ok, err)
	}
	list, err = s.Recent(ctx, 2)
	if err != nil || len(list) != 2 || list[0].Metric != "humidity" {
		t.Fatal(list, err)
	}
}
func TestMeasuredTimeAndConflict(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	r := sample()
	r.MeasuredAt = &now
	if _, err := s.Insert(ctx, r, now); err != nil {
		t.Fatal(err)
	}
	same := now.In(time.FixedZone("other", 3600))
	r.MeasuredAt = &same
	if ok, err := s.Insert(ctx, r, now); ok || err != nil {
		t.Fatal(ok, err)
	}
	later := now.Add(time.Second)
	r.MeasuredAt = &later
	if _, err := s.Insert(ctx, r, now); !errors.Is(err, telemetry.ErrConflict) {
		t.Fatal(err)
	}
	r.MeasuredAt = &same
	r.Unit = "K"
	if _, err := s.Insert(ctx, r, now); !errors.Is(err, telemetry.ErrConflict) {
		t.Fatal(err)
	}
}
func TestConcurrentRetry(t *testing.T) {
	s := openTest(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Insert(context.Background(), sample(), time.Now()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	list, err := s.Recent(context.Background(), 100)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
}
func TestErrorsAndLimits(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert(ctx, telemetry.Reading{}, time.Now()); !errors.Is(err, telemetry.ErrInvalid) {
		t.Fatal(err)
	}
	for _, n := range []int{0, 101} {
		if _, err := s.Recent(ctx, n); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	list, err := s.Recent(ctx, 100)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatal(list, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Insert(canceled, sample(), time.Now()); err == nil {
		t.Fatal("accepted canceled operation")
	}
	_ = s.Close()
	if s.Ping(ctx) == nil {
		t.Fatal("closed database healthy")
	}
	if _, err = s.Recent(ctx, 1); err == nil {
		t.Fatal("closed database query succeeded")
	}
	if _, err = Open(filepath.Join(t.TempDir(), "missing", "test.db")); err == nil {
		t.Fatal("invalid path accepted")
	}
}
func TestRejectNewerSchema(t *testing.T) {
	s := openTest(t)
	if _, err := s.db.Exec("PRAGMA user_version=6"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err == nil {
		t.Fatal("newer schema accepted")
	}
}
