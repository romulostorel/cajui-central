package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/cajui/cajui-central/internal/commands"
	"github.com/cajui/cajui-central/internal/devicestate"
)

// Option configures optional server features.
type Option func(*server)

// WithCommands lets the local page send management channel commands through publisher.
func WithCommands(publisher commands.Publisher) Option {
	return func(s *server) { s.publisher = publisher }
}

// sendCommand is a local write like workspace edits: same origin plus the page capability.
func (s *server) sendCommand(w http.ResponseWriter, r *http.Request) {
	if !s.trustedLocalWrite(r) {
		http.Error(w, "reload the local page before sending commands", http.StatusForbidden)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "use application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var input struct {
		SourceID string `json:"source_id"`
		DeviceID string `json:"device_id"`
		Type     string `json:"type"`
		NodeID   string `json:"node_id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid command", 400)
		return
	}
	// A closed tab must not turn a command already handed to the broker into a failure.
	record, err := commands.Send(context.WithoutCancel(r.Context()), s.repo, s.publisher, input.SourceID, input.DeviceID, input.Type, input.NodeID, time.Now())
	switch {
	case errors.Is(err, devicestate.ErrInvalid):
		http.Error(w, "command not offered by this device", 400)
	case errors.Is(err, devicestate.ErrUnknownDevice):
		http.Error(w, "device not found", 404)
	case errors.Is(err, commands.ErrUnavailable):
		http.Error(w, "broker connection unavailable", 503)
	case err != nil:
		s.fail(w, err)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(record)
	}
}
func (s *server) commandStatus(w http.ResponseWriter, r *http.Request) {
	record, err := commands.Status(r.Context(), s.repo, r.PathValue("id"), time.Now())
	if errors.Is(err, devicestate.ErrUnknownCommand) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(record)
}
