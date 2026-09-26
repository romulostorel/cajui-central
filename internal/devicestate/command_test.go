package devicestate

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNewCommandFollowsTheAdvertisedCapabilities(t *testing.T) {
	receiver := State{Role: "receiver", Capabilities: []string{"pairing", "revoke"}}
	for _, tc := range []struct{ kind, node string }{
		{"pairing.open", ""}, {"pairing.close", ""}, {"pairing.accept", "00000000000000a2"}, {"node.revoke", "00000000000000a2"},
	} {
		c, err := NewCommand(tc.kind, tc.node, receiver)
		if err != nil || !strings.HasPrefix(c.ID, "central-") || !commandID.MatchString(c.ID) || c.Type != tc.kind || c.NodeID != tc.node {
			t.Fatal(tc, c, err)
		}
	}
	a, _ := NewCommand("pairing.open", "", receiver)
	b, _ := NewCommand("pairing.open", "", receiver)
	if a.ID == b.ID {
		t.Fatal("command IDs repeat")
	}
	pairingOnly := State{Role: "receiver", Capabilities: []string{"pairing"}}
	for _, tc := range []struct {
		kind, node string
		state      State
	}{
		{"pairing.open", "00000000000000a2", receiver},
		{"pairing.accept", "", receiver},
		{"pairing.accept", "A2", receiver},
		{"firmware.install", "", receiver},
		{"node.revoke", "00000000000000a2", pairingOnly},
		{"pairing.open", "", State{Role: "receiver"}},
		{"pairing.open", "", State{Role: "transmitter", Capabilities: []string{"pairing"}}},
	} {
		if _, err := NewCommand(tc.kind, tc.node, tc.state); !errors.Is(err, ErrInvalid) {
			t.Error(tc.kind, tc.node, err)
		}
	}
}

func TestCommandPayloadMatchesTheContract(t *testing.T) {
	for _, tc := range []struct {
		command Command
		want    string
	}{
		{Command{ID: "central-1", Type: "pairing.open"}, `{"version":1,"command_id":"central-1","type":"pairing.open","params":{}}`},
		{Command{ID: "central-2", Type: "node.revoke", NodeID: "00000000000000a2"}, `{"version":1,"command_id":"central-2","type":"node.revoke","params":{"node_id":"00000000000000a2"}}`},
	} {
		got, err := tc.command.Payload()
		if err != nil || string(got) != tc.want {
			t.Fatal(string(got), err)
		}
	}
}

func TestDecodeResult(t *testing.T) {
	r, err := DecodeResult([]byte(`{"version":1,"command_id":"central-1","status":"rejected","reason":"not_requested"}`))
	if err != nil || r.CommandID != "central-1" || r.Status != "rejected" || *r.Reason != "not_requested" {
		t.Fatal(r, err)
	}
	r, err = DecodeResult([]byte(`{"version":1,"command_id":"c","status":"rejected","reason":"brand_new_reason"}`))
	if err != nil || *r.Reason != "failed" {
		t.Fatal(r, err)
	}
	for _, status := range []string{"applied", "pending"} {
		b, _ := json.Marshal(map[string]any{"version": 1, "command_id": "c", "status": status, "reason": nil})
		if r, err = DecodeResult(b); err != nil || r.Reason != nil {
			t.Fatal(status, r, err)
		}
	}
	for _, payload := range []string{
		``, `[]`, `{"version":2,"command_id":"c","status":"applied","reason":null}`,
		`{"version":1,"command_id":"bad id","status":"applied","reason":null}`,
		`{"version":1,"command_id":"c","status":"done","reason":null}`,
		`{"version":1,"command_id":"c","status":"applied","reason":"busy"}`,
		`{"version":1,"command_id":"c","status":"rejected","reason":null}`,
		`{"version":1,"command_id":"c","status":"rejected","reason":"Not Valid"}`,
		`{"version":1,"command_id":"c","status":"applied","reason":null} {}`,
		`{"version":1,"command_id":"` + strings.Repeat("c", MaxStateBytes) + `","status":"applied"}`,
	} {
		if _, err := DecodeResult([]byte(payload)); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %.60s", payload)
		}
	}
	if _, _, kind, ok := ParseTopic(Topic("r", "00000000000000d1", "results")); !ok || kind != "results" {
		t.Fatal(kind, ok)
	}
}
