package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cajui/cajui-central/internal/telemetry"
)

func sampleFixture(t *testing.T) telemetry.Sample {
	t.Helper()
	b, e := os.ReadFile("../../examples/mqtt/sample.json")
	if e != nil {
		t.Fatal(e)
	}
	s, e := telemetry.DecodeSample(b)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestSamplesPersistenceAndFreshness(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	s := sampleFixture(t)
	now := time.Now().UTC()
	if ok, e := db.InsertSample(ctx, s, now); !ok || e != nil {
		t.Fatal(ok, e)
	}
	if ok, e := db.InsertSample(ctx, s, now.Add(time.Hour)); ok || e != nil {
		t.Fatal(ok, e)
	}
	s.Readings[0].Unit = "other"
	if _, e := db.InsertSample(ctx, s, now); !errors.Is(e, telemetry.ErrConflict) {
		t.Fatal(e)
	}
	if e := db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	all, e := db.RecentSamples(ctx, 100)
	if e != nil || len(all) != 1 || !all[0].ReceivedAt.Equal(now) {
		t.Fatal(all, e)
	}
	for _, tc := range []struct {
		delay time.Duration
		stale bool
	}{{899 * time.Second, false}, {900 * time.Second, true}} {
		d, e := db.Devices(ctx, now.Add(tc.delay))
		if e != nil || len(d) != 1 || d[0].Stale != tc.stale {
			t.Fatal(d, e)
		}
	}
	s.SampleID = "new"
	s.Readings[0].Status = "error"
	s.Readings[0].Value = nil
	if _, e = db.InsertSample(ctx, s, now.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	d, e := db.Devices(ctx, now.Add(time.Hour))
	if e != nil || d[0].Stale || !d[0].SensorError {
		t.Fatal(d, e)
	}
}
func TestSampleFailuresAndConcurrency(t *testing.T) {
	db, e := Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	s := sampleFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := db.InsertSample(ctx, s, time.Now()); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	all, e := db.RecentSamples(ctx, 100)
	if e != nil || len(all) != 1 {
		t.Fatal(all, e)
	}
	for _, limit := range []int{0, 101} {
		if _, e := db.RecentSamples(ctx, limit); e == nil {
			t.Fatal("invalid limit")
		}
	}
	s.Version = 0
	if _, e := db.InsertSample(ctx, s, time.Now()); e == nil {
		t.Fatal("invalid accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	s.Version = 1
	if _, e := db.InsertSample(canceled, s, time.Now()); e == nil {
		t.Fatal("canceled insert")
	}
	if _, e := db.RecentSamples(canceled, 100); e == nil {
		t.Fatal("canceled query")
	}
	if _, e := db.Devices(canceled, time.Now()); e == nil {
		t.Fatal("canceled devices")
	}
	if _, e := db.db.Exec(`UPDATE samples SET payload='invalid'`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.RecentSamples(ctx, 100); e == nil {
		t.Fatal("corrupt payload accepted")
	}
	if _, e := db.db.Exec(`UPDATE samples SET payload='{}',received_at='invalid'`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.RecentSamples(ctx, 100); e == nil {
		t.Fatal("corrupt timestamp accepted")
	}
}
func TestMigrationPreservesVersionOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	r := telemetry.Reading{NodeID: "n", SensorID: "s", SessionID: "b", Metric: "t", Unit: "C", Value: 1}
	if _, e = db.Insert(context.Background(), r, time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e = db.db.Exec(`DROP TABLE workspace_measurements; DROP TABLE workspace_sensors; DROP TABLE workspace_devices; DROP TABLE workspace_layout; DROP TABLE samples; DROP TABLE device_states; DROP TABLE device_availability; PRAGMA user_version=1`); e != nil {
		t.Fatal(e)
	}
	db.Close()
	db, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	old, e := db.Recent(context.Background(), 100)
	if e != nil || len(old) != 1 {
		t.Fatal(old, e)
	}
	if _, e = db.InsertSample(context.Background(), sampleFixture(t), time.Now()); e != nil {
		t.Fatal(e)
	}
}
