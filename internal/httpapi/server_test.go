package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cajui/cajui-central/internal/devicestate"
	"github.com/cajui/cajui-central/internal/storage"
	"github.com/cajui/cajui-central/internal/telemetry"
	"github.com/cajui/cajui-central/internal/workspace"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const token = "test-token-not-for-production-123456"
const payload = `{"node_id":"node","sensor_id":"sensor","session_id":"boot","sequence":0,"metric":"temperature","value":0,"unit":"degC"}`

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h, err := New(s, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func request(h http.Handler, method, path, body, auth, content string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	r.Header.Set("Authorization", auth)
	r.Header.Set("Content-Type", content)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestIngestionAndDashboard(t *testing.T) {
	h := testHandler(t)
	if w := request(h, "GET", "/", "", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "No readings") {
		t.Fatal(w.Code, w.Body)
	}
	for _, status := range []int{201, 200} {
		w := request(h, "POST", "/api/v1/readings", payload, "Bearer "+token, "application/json")
		if w.Code != status {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := request(h, "GET", "/api/v1/readings", "", "Bearer "+token, "")
	var list []telemetry.Reading
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list) != 1 || list[0].Value != 0 || list[0].ReceivedAt.IsZero() {
		t.Fatal(w.Code, list, err)
	}
	w = request(h, "POST", "/api/v1/readings", strings.Replace(payload, `"value":0`, `"value":1`, 1), "Bearer "+token, "application/json")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	w = request(h, "GET", "/", "", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "temperature") {
		t.Fatal(w.Code, w.Body)
	}
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
	if w = request(h, "GET", "/healthz", "", "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request(h, "GET", "/unknown", "", "", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w = request(h, "DELETE", "/api/v1/readings", "", "Bearer "+token, ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
}
func TestRejectedPayloads(t *testing.T) {
	h := testHandler(t)
	for name, body := range map[string]string{
		"malformed": "{", "null": "null", "missing value": strings.Replace(payload, `"value":0,`, "", 1), "missing sequence": strings.Replace(payload, `"sequence":0,`, "", 1),
		"unknown": strings.Replace(payload, `"value":0`, `"value":0,"unexpected":1`, 1), "client receipt": strings.Replace(payload, `"value":0`, `"value":0,"received_at":"2026-09-22T00:00:00Z"`, 1),
		"multiple": payload + payload, "invalid id": strings.Replace(payload, `"node"`, `"node/invalid"`, 1), "too large": strings.Repeat(" ", 8193) + payload,
		"null value": strings.Replace(payload, `"value":0`, `"value":null`, 1), "invalid date": strings.Replace(payload, `"value":0`, `"value":0,"measured_at":"yesterday"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			w := request(h, "POST", "/api/v1/readings", body, "Bearer "+token, "application/json")
			if w.Code != 400 {
				t.Fatal(w.Code, w.Body)
			}
		})
	}
	if w := request(h, "POST", "/api/v1/readings", payload, "Bearer "+token, "text/plain"); w.Code != 415 {
		t.Fatal(w.Code)
	}
	for _, method := range []string{"GET", "POST"} {
		for _, auth := range []string{"", "Bearer wrong", "Basic " + token, token} {
			if w := request(h, method, "/api/v1/readings", payload, auth, "application/json"); w.Code != 401 {
				t.Fatal(w.Code)
			}
		}
	}
	if _, err := New(nil, "short", nil); err == nil {
		t.Fatal("short token accepted")
	}
}

type brokenRepo struct{}

func (brokenRepo) Insert(context.Context, telemetry.Reading, time.Time) (bool, error) {
	return false, errors.New("private database failure")
}
func (brokenRepo) Recent(context.Context, int) ([]telemetry.Reading, error) {
	return nil, errors.New("private database failure")
}
func (brokenRepo) Ping(context.Context) error { return errors.New("private database failure") }
func TestStorageFailure(t *testing.T) {
	h, err := New(brokenRepo{}, token, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/api/v1/readings", "/healthz"} {
		w := request(h, "GET", path, "", "Bearer "+token, "")
		want := 500
		if path == "/healthz" {
			want = 503
		}
		if w.Code != want || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := request(h, "POST", "/api/v1/readings", payload, "Bearer "+token, "application/json"); w.Code != 500 {
		t.Fatal(w.Code)
	}
}

func (brokenRepo) RecentSamples(context.Context, int) ([]telemetry.StoredSample, error) {
	return nil, errors.New("database failure")
}
func (brokenRepo) DeviceStates(context.Context) ([]devicestate.Stored, error) {
	return nil, errors.New("private database failure")
}
func (brokenRepo) Devices(context.Context, time.Time) ([]telemetry.Device, error) {
	return nil, errors.New("database failure")
}

func TestMQTTSampleRoutesAndDashboard(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	value := 21.5
	sample := telemetry.Sample{Version: 1, SourceID: "source", DeviceID: "device", SampleID: "one", ExpectedIntervalSeconds: 1, Readings: []telemetry.Measurement{{SensorID: "ambient", Metric: "temperature", Value: &value, Unit: "degC", Status: "ok"}}}
	if _, err = db.InsertSample(context.Background(), sample, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	stateJSON := `{"version":1,"source_id":"source","device_id":"00000000000000d1","role":"receiver","model":"bench-<receiver>"}`
	state, err := devicestate.Decode("source", "00000000000000d1", []byte(stateJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveDeviceState(context.Background(), state, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	h, err := New(db, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/samples", "/api/v1/devices", "/api/v1/device-states"} {
		req := httptest.NewRequest("GET", path, nil)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != 401 {
			t.Fatal(out.Code)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		out = httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != 200 || !strings.Contains(out.Body.String(), "device") {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "http://localhost/", nil))
	for _, want := range []string{"No recent samples", "21.5", "source", `"device_states":[{`, `bench-\u003creceiver\u003e`} {
		if !strings.Contains(out.Body.String(), want) {
			t.Fatal(want, out.Body.String())
		}
	}
	if strings.Contains(out.Body.String(), "bench-<receiver>") {
		t.Fatal("device text reached the page unescaped")
	}
	broken, _ := New(brokenRepo{}, token, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, path := range []string{"/api/v1/samples", "/api/v1/devices", "/api/v1/device-states"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		out = httptest.NewRecorder()
		broken.ServeHTTP(out, req)
		if out.Code != 500 {
			t.Fatal(out.Code)
		}
	}
}

func (brokenRepo) Catalog(context.Context) (workspace.Catalog, error) {
	return workspace.Catalog{}, errors.New("private database failure")
}
func (brokenRepo) SaveDevice(context.Context, int64, workspace.Settings) error {
	return errors.New("private database failure")
}
func (brokenRepo) SaveSensor(context.Context, int64, workspace.Settings) error {
	return errors.New("private database failure")
}
func (brokenRepo) SaveLayout(context.Context, workspace.Layout) error {
	return errors.New("private database failure")
}
