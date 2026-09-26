package httpapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/cajui/cajui-central/internal/workspace"
)

// The local UI is a trusted local-user surface, not a network login. Reject
// rebinding hosts and require both a same-origin browser request and a separate
// process-scoped capability. The ingestion credential is never sent to a page.
func localHost(host string) bool {
	u, err := url.Parse("http://" + host)
	if err != nil || u.Host != host || u.User != nil || u.Path != "" {
		return false
	}
	name := u.Hostname()
	return name == "localhost" || net.ParseIP(name) != nil && net.ParseIP(name).IsLoopback()
}
func (s *server) localPage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			http.Error(w, "local workspace access required", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// trustedLocalWrite accepts a same-origin request from the local page that carries the
// process-scoped capability; anything else could be another site forging it (CSRF).
func (s *server) trustedLocalWrite(r *http.Request) bool {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return localHost(r.Host) && r.Header.Get("Origin") == scheme+"://"+r.Host &&
		subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Cajui-Workspace")), []byte(s.uiToken)) == 1 &&
		(r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin")
}
func (s *server) editWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.trustedLocalWrite(r) {
		http.Error(w, "reload the local page before editing", http.StatusForbidden)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "use application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	var err error
	decode := func(target any) error {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return workspace.ErrInvalid
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || fields == nil || fields["revision"] == nil || bytes.Equal(bytes.TrimSpace(fields["revision"]), []byte("null")) {
			return workspace.ErrInvalid
		}
		if _, ok := target.(*workspace.Layout); ok && fields["sections"] == nil {
			return workspace.ErrInvalid
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(target); err != nil {
			return workspace.ErrInvalid
		}
		if dec.Decode(new(any)) != io.EOF {
			return workspace.ErrInvalid
		}
		return nil
	}
	if r.PathValue("kind") == "dashboard" && r.PathValue("id") == "layout" {
		var layout workspace.Layout
		err = decode(&layout)
		if err == nil {
			err = s.repo.SaveLayout(r.Context(), layout)
		}
	} else {
		kind := r.PathValue("kind")
		if kind != "devices" && kind != "sensors" {
			http.NotFound(w, r)
			return
		}
		id, parseErr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if parseErr != nil || id <= 0 {
			http.Error(w, "invalid item", 400)
			return
		}
		var settings workspace.Settings
		err = decode(&settings)
		if err == nil {
			if kind == "devices" {
				err = s.repo.SaveDevice(r.Context(), id, settings)
			} else {
				err = s.repo.SaveSensor(r.Context(), id, settings)
			}
		}
	}
	switch {
	case errors.Is(err, workspace.ErrInvalid):
		http.Error(w, "invalid workspace settings", 400)
	case errors.Is(err, workspace.ErrNotFound):
		http.Error(w, "observed item not found", 404)
	case errors.Is(err, workspace.ErrConflict):
		http.Error(w, "settings changed in another window; reload before saving", 409)
	case errors.Is(err, workspace.ErrUnregistered):
		http.Error(w, "register the device or sensor first", 409)
	case err != nil:
		s.fail(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
