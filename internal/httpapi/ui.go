package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"time"

	"github.com/cajui/cajui-central/internal/devicestate"
	"github.com/cajui/cajui-central/internal/telemetry"
	"github.com/cajui/cajui-central/internal/workspace"
)

// Assets are plain source files: no frontend build or external runtime request.
//
//go:embed ui
var uiFiles embed.FS

type dashboardState struct {
	Locale    string                   `json:"locale"`
	Workspace *workspace.Catalog       `json:"workspace,omitempty"`
	UIToken   string                   `json:"ui_token,omitempty"`
	Readings  []telemetry.Reading      `json:"readings"`
	Samples   []telemetry.StoredSample `json:"samples"`
	Devices   []telemetry.Device       `json:"devices"`
	// Latest management state of cajui-firmware devices (cajui-firmware docs/management-v1.md).
	DeviceStates []devicestate.Stored `json:"device_states"`
	GeneratedAt  time.Time            `json:"generated_at"`
}
type dashboardPage struct {
	Title string
	Route string
	State dashboardState
}

func serveUIAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("path")
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	data, err := uiFiles.ReadFile("ui/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	types := map[string]string{".css": "text/css; charset=utf-8", ".mjs": "text/javascript; charset=utf-8", ".svg": "image/svg+xml", ".ttf": "font/ttf", ".txt": "text/plain; charset=utf-8"}
	contentType, ok := types[path.Ext(name)]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}
