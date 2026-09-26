// Package httpapi exposes the local API and embedded monitoring page.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cajui/cajui-central/internal/devicestate"
	"github.com/cajui/cajui-central/internal/telemetry"
	"github.com/cajui/cajui-central/internal/workspace"
)

type Repository interface {
	Catalog(context.Context) (workspace.Catalog, error)
	SaveDevice(context.Context, int64, workspace.Settings) error
	SaveSensor(context.Context, int64, workspace.Settings) error
	SaveLayout(context.Context, workspace.Layout) error
	Insert(context.Context, telemetry.Reading, time.Time) (bool, error)
	Recent(context.Context, int) ([]telemetry.Reading, error)
	Ping(context.Context) error
	RecentSamples(context.Context, int) ([]telemetry.StoredSample, error)
	Devices(context.Context, time.Time) ([]telemetry.Device, error)
	DeviceStates(context.Context) ([]devicestate.Stored, error)
}

//go:embed index.html
var page string
var dashboard = template.Must(template.New("index").Parse(page))

type server struct {
	repo    Repository
	uiToken string
	token   [32]byte
	logger  *slog.Logger
}

func New(repo Repository, token string, logger *slog.Logger) (http.Handler, error) {
	if len(token) < 24 {
		return nil, errors.New("API token must contain at least 24 characters")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &server{repo: repo, token: sha256.Sum256([]byte(token)), logger: logger, uiToken: rand.Text()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /{$}", s.localPage(s.index))
	mux.HandleFunc("GET /devices", s.localPage(s.index))
	mux.HandleFunc("GET /sensors", s.localPage(s.index))
	mux.HandleFunc("PUT /ui-api/{kind}/{id}", s.editWorkspace)
	mux.HandleFunc("GET /ui/{path...}", serveUIAsset)

	mux.Handle("GET /api/v1/readings", s.authorize(http.HandlerFunc(s.list)))
	mux.Handle("GET /api/v1/samples", s.authorize(http.HandlerFunc(s.samples)))
	mux.Handle("GET /api/v1/devices", s.authorize(http.HandlerFunc(s.devices)))
	mux.Handle("GET /api/v1/device-states", s.authorize(http.HandlerFunc(s.deviceStates)))
	mux.Handle("POST /api/v1/readings", s.authorize(http.HandlerFunc(s.ingest)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'none'")
		mux.ServeHTTP(w, r)
	}), nil
}
func (s *server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		got := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
		if !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare(got[:], s.token[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *server) fail(w http.ResponseWriter, err error) {
	s.logger.Error("request failed", "error", err)
	http.Error(w, "internal server error", 500)
}
func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.repo.Ping(r.Context()); err != nil {
		http.Error(w, "not ready", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}
func (s *server) list(w http.ResponseWriter, r *http.Request) {
	readings, err := s.repo.Recent(r.Context(), 100)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(w).Encode(readings); err != nil {
		s.logger.Error("encode response", "error", err)
	}
}

// Pages and workspace edits are local; ingestion APIs require the API token.
func (s *server) index(w http.ResponseWriter, r *http.Request) {
	readings, err := s.repo.Recent(r.Context(), 100)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	samples, err := s.repo.RecentSamples(r.Context(), 100)
	if err != nil {
		s.fail(w, err)
		return
	}
	devices, err := s.repo.Devices(r.Context(), time.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	catalog, err := s.repo.Catalog(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	states, err := s.repo.DeviceStates(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	language := requestLocale(w, r)
	w.Header().Set("Content-Language", language)
	w.Header().Add("Vary", "Accept-Language")
	w.Header().Add("Vary", "Cookie")
	title := "common.dashboard"
	if r.URL.Path == "/devices" {
		title = "common.devices"
	}
	if r.URL.Path == "/sensors" {
		title = "common.sensors"
	}
	if err = dashboard.Execute(w, dashboardPage{Title: catalogs[language][title], Route: r.URL.Path, State: dashboardState{Locale: language, Readings: readings, Samples: samples, Devices: devices, DeviceStates: states, Workspace: &catalog, UIToken: s.uiToken, GeneratedAt: time.Now().UTC()}}); err != nil {
		s.logger.Error("render dashboard", "error", err)
	}
}
func (s *server) ingest(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "use application/json", 415)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	// Pointer value distinguishes a missing measurement from a legitimate zero.
	var input struct {
		NodeID     string     `json:"node_id"`
		SensorID   string     `json:"sensor_id"`
		SessionID  string     `json:"session_id"`
		Sequence   *int64     `json:"sequence"`
		Metric     string     `json:"metric"`
		Value      *float64   `json:"value"`
		Unit       string     `json:"unit"`
		MeasuredAt *time.Time `json:"measured_at,omitempty"`
	}
	if err := dec.Decode(&input); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		http.Error(w, "expected one JSON object", 400)
		return
	}
	if input.Value == nil || input.Sequence == nil {
		http.Error(w, "value and sequence are required", 400)
		return
	}
	reading := telemetry.Reading{NodeID: input.NodeID, SensorID: input.SensorID, SessionID: input.SessionID, Sequence: *input.Sequence, Metric: input.Metric, Value: *input.Value, Unit: input.Unit, MeasuredAt: input.MeasuredAt}
	if err := reading.Validate(); err != nil {
		http.Error(w, "invalid reading", 400)
		return
	}
	created, err := s.repo.Insert(r.Context(), reading, time.Now())
	if errors.Is(err, telemetry.ErrConflict) {
		http.Error(w, "conflicting event identity", 409)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"status":"created"}`)
	} else {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"status":"duplicate"}`)
	}
}

func (s *server) samples(w http.ResponseWriter, r *http.Request) {
	samples, err := s.repo.RecentSamples(r.Context(), 100)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(samples)
}
func (s *server) devices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.repo.Devices(r.Context(), time.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(devices)
}
func (s *server) deviceStates(w http.ResponseWriter, r *http.Request) {
	states, err := s.repo.DeviceStates(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(states)
}
