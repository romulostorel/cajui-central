package devicestate

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../examples/mqtt/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeReceiverAndTransmitter(t *testing.T) {
	r, err := Decode("demo-source", "00000000000000d1", fixture(t, "receiver-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Role != "receiver" || *r.Firmware.Version != "0.2.0" || *r.Queue.Capacity != 128 || *r.WiFi.RSSIDBm != -61 || r.Pairing.Open || len(r.Pairing.Requests) != 0 {
		t.Fatalf("%+v", r)
	}
	tx, err := Decode("demo-source", "00000000000000d2", fixture(t, "transmitter-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if *tx.Binding != "active" || *tx.LastFrame.SNRDB != 9.5 || tx.Model != nil || tx.Firmware != nil || tx.Parameters.IntervalS != nil {
		t.Fatalf("%+v", tx)
	}
}

func TestUnknownFieldsAreIgnoredAndNullsStayUnknown(t *testing.T) {
	payload := `{"version":1,"source_id":"s","device_id":"00000000000000d1","role":"receiver","future":{"x":1},"model":null,"wifi":{"rssi_dbm":null}}`
	s, err := Decode("s", "00000000000000d1", []byte(payload))
	if err != nil || s.Model != nil || s.WiFi == nil || s.WiFi.RSSIDBm != nil {
		t.Fatal(s, err)
	}
}

func TestInvalidStatesAreRejected(t *testing.T) {
	base := `"version":1,"source_id":"s","device_id":"00000000000000d1"`
	tx := base + `,"role":"transmitter","receiver_id":"00000000000000d0","binding":"active"`
	for _, payload := range []string{
		`not json`,
		`[]`,
		`{` + base + `,"role":"receiver"} {}`,
		`{"version":2,"source_id":"s","device_id":"00000000000000d1","role":"receiver"}`,
		`{"version":1,"source_id":"other","device_id":"00000000000000d1","role":"receiver"}`,
		`{"version":1,"source_id":"s","device_id":"00000000000000d2","role":"receiver"}`,
		`{` + base + `,"role":"gateway"}`,
		`{` + base + `,"role":"receiver","binding":"active"}`,
		`{` + base + `,"role":"receiver","model":"bad\nmodel"}`,
		`{` + base + `,"role":"receiver","model":""}`,
		`{` + base + `,"role":"receiver","model":"` + strings.Repeat("m", 65) + `"}`,
		`{` + base + `,"role":"receiver","firmware":{"version":"1.2"}}`,
		`{` + base + `,"role":"receiver","firmware":{"slot":"OTA 0"}}`,
		`{` + base + `,"role":"receiver","reset_reason":"<script>"}`,
		`{` + base + `,"role":"receiver","uptime_s":-1}`,
		`{` + base + `,"role":"receiver","uptime_s":1.5}`,
		`{` + base + `,"role":"receiver","radio":{"power_dbm":200}}`,
		`{` + base + `,"role":"receiver","radio":{"profile":70000}}`,
		`{` + base + `,"role":"receiver","wifi":{"rssi_dbm":10}}`,
		`{` + base + `,"role":"receiver","queue":{"depth":-1}}`,
		`{` + base + `,"role":"receiver","forwarding":{"retries":-1}}`,
		`{` + base + `,"role":"receiver","pairing":{"open":true,"remaining_s":90000,"requests":[]}}`,
		`{` + base + `,"role":"receiver","pairing":{"open":true,"requests":[{"node_id":"xyz","conflict":false}]}}`,
		`{` + base + `,"role":"receiver","pairing":{"open":true,"requests":[{"node_id":"00000000000000a1","rssi_dbm":5,"conflict":false}]}}`,
		`{` + base + `,"role":"receiver","pairing":{"open":true,"requests":[` + strings.Repeat(`{"node_id":"00000000000000a1","conflict":false},`, 16) + `{"node_id":"00000000000000a1","conflict":false}]}}`,
		`{` + base + `,"role":"receiver","capabilities":["Pairing"]}`,
		`{` + base + `,"role":"receiver","capabilities":[` + strings.Repeat(`"a",`, 16) + `"a"]}`,
		`{` + base + `,"role":"transmitter","binding":"active"}`,
		`{` + base + `,"role":"transmitter","receiver_id":"00000000000000d0"}`,
		`{` + tx[len(base)+1:] + `}`,
		`{` + base + `,"role":"transmitter","receiver_id":"00000000000000d0","binding":"lost"}`,
		`{` + tx + `,"queue":{"depth":1}}`,
		`{` + tx + `,"last_frame":{"counter":-1}}`,
		`{` + tx + `,"last_frame":{"rssi_dbm":1}}`,
		`{` + tx + `,"last_frame":{"snr_db":99}}`,
		`{` + tx + `,"last_frame":{"receiver_uptime_s":-5}}`,
		`{` + tx + `,"parameters":{"interval_s":0}}`,
		`{` + tx + `,"parameters":{"power_dbm":-200}}`,
		string([]byte{0xff}),
		`{` + base + `,"role":"receiver","model":"` + strings.Repeat("x", MaxStateBytes) + `"}`,
	} {
		if _, err := Decode("s", "00000000000000d1", []byte(payload)); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %.80s", payload)
		}
	}
	if _, err := Decode("bad source", "00000000000000d1", []byte(`{"version":1,"source_id":"bad source","device_id":"00000000000000d1","role":"receiver"}`)); !errors.Is(err, ErrInvalid) {
		t.Error("accepted an invalid source")
	}
}

func TestTopicsAndAvailability(t *testing.T) {
	topic := Topic("receiver-1", "00000000000000d1", "state")
	if topic != "manage/v1/receiver-1/00000000000000d1/state" {
		t.Fatal(topic)
	}
	if s, d, k, ok := ParseTopic(topic); !ok || s != "receiver-1" || d != "00000000000000d1" || k != "state" {
		t.Fatal(s, d, k, ok)
	}
	if _, _, k, ok := ParseTopic(Topic("r", "00000000000000d1", "availability")); !ok || k != "availability" {
		t.Fatal(k, ok)
	}
	for _, bad := range []string{
		"manage/v1/r/00000000000000d1/commands",
		"manage/v2/r/00000000000000d1/state",
		"manage/v1/r:1/00000000000000d1/state",
		"manage/v1/r/00000000000000D1/state",
		"manage/v1/r/00000000000000d1/state/extra",
		"telemetry/v1/r/00000000000000d1/samples",
	} {
		if _, _, _, ok := ParseTopic(bad); ok {
			t.Error(bad)
		}
	}
	for _, value := range []string{"online", "offline"} {
		if got, err := DecodeAvailability([]byte(value)); err != nil || got != value {
			t.Error(value, err)
		}
	}
	for _, value := range []string{"", "Online", "offline\n", "1"} {
		if _, err := DecodeAvailability([]byte(value)); !errors.Is(err, ErrInvalid) {
			t.Error(value)
		}
	}
}
