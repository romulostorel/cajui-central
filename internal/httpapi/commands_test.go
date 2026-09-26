package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cajui/cajui-central/internal/commands"
	"github.com/cajui/cajui-central/internal/devicestate"
	"github.com/cajui/cajui-central/internal/storage"
)

type fakePublisher struct {
	err  error
	sent []devicestate.Command
}

func (p *fakePublisher) PublishCommand(_ context.Context, _, _ string, c devicestate.Command) error {
	p.sent = append(p.sent, c)
	return p.err
}

func commandRequest(h http.Handler, body, capability string, alter func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://localhost/ui-api/commands", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://localhost")
	r.Header.Set("X-Cajui-Workspace", capability)
	if alter != nil {
		alter(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCommandRoutes(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state, err := devicestate.Decode("r", "00000000000000d1", []byte(`{"version":1,"source_id":"r","device_id":"00000000000000d1","role":"receiver","capabilities":["pairing","revoke"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveDeviceState(context.Background(), state, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{}
	h, err := New(db, token, slog.New(slog.NewTextHandler(io.Discard, nil)), WithCommands(publisher))
	if err != nil {
		t.Fatal(err)
	}
	capability := workspaceSnapshot(t, h, "/").UIToken
	body := `{"source_id":"r","device_id":"00000000000000d1","type":"pairing.accept","node_id":"00000000000000a2"}`
	for name, alter := range map[string]func(*http.Request){
		"no capability":  func(r *http.Request) { r.Header.Del("X-Cajui-Workspace") },
		"foreign origin": func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"cross site":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		if w := commandRequest(h, body, capability, alter); w.Code != 403 {
			t.Fatal(name, w.Code)
		}
	}
	if len(publisher.sent) != 0 {
		t.Fatal("a forged request published a command")
	}
	if w := commandRequest(h, body, capability, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }); w.Code != 415 {
		t.Fatal(w.Code)
	}
	for _, bad := range []string{`{`, `{"source_id":"r","extra":1}`, body + `{}`} {
		if w := commandRequest(h, bad, capability, nil); w.Code != 400 {
			t.Fatal(bad, w.Code)
		}
	}
	if w := commandRequest(h, `{"source_id":"r","device_id":"00000000000000d1","type":"firmware.install"}`, capability, nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := commandRequest(h, `{"source_id":"r","device_id":"00000000000000ff","type":"pairing.open"}`, capability, nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	w := commandRequest(h, body, capability, nil)
	if w.Code != 202 || len(publisher.sent) != 1 {
		t.Fatal(w.Code, w.Body)
	}
	var record devicestate.CommandRecord
	if err = json.Unmarshal(w.Body.Bytes(), &record); err != nil || record.Status != "sent" || record.ID != publisher.sent[0].ID {
		t.Fatal(record, err)
	}
	status := func(id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/ui-api/commands/"+id, nil))
		return w
	}
	if w = status(record.ID); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"sent"`) {
		t.Fatal(w.Code, w.Body)
	}
	if w = status("missing"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	publisher.err = commands.ErrUnavailable
	if w = commandRequest(h, body, capability, nil); w.Code != 503 {
		t.Fatal(w.Code)
	}
	// Without MQTT there is nothing to send commands through.
	plain, _ := New(db, token, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if w = commandRequest(plain, body, workspaceSnapshot(t, plain, "/").UIToken, nil); w.Code != 503 {
		t.Fatal(w.Code)
	}
	broken, _ := New(brokenRepo{}, token, slog.New(slog.NewTextHandler(io.Discard, nil)), WithCommands(publisher))
	w = httptest.NewRecorder()
	broken.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/ui-api/commands/x", nil))
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
}
