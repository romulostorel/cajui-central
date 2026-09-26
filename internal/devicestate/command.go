package devicestate

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"
)

// Command types of the management channel that Central sends. Each belongs to a
// capability family the device must list in its state before Central offers it.
var commandFamilies = map[string]string{
	"pairing.open":   "pairing",
	"pairing.accept": "pairing",
	"pairing.close":  "pairing",
	"node.revoke":    "revoke",
}

// Command is one outgoing command. NodeID is required by pairing.accept and node.revoke
// and forbidden otherwise.
type Command struct {
	ID     string
	Type   string
	NodeID string
}

// NewCommand validates a command for a device and gives it a fresh random ID.
func NewCommand(kind, node string, state State) (Command, error) {
	family, ok := commandFamilies[kind]
	needsNode := kind == "pairing.accept" || kind == "node.revoke"
	if !ok || state.Role != "receiver" || needsNode != (node != "") || (node != "" && !deviceID.MatchString(node)) {
		return Command{}, ErrInvalid
	}
	offered := false
	for _, c := range state.Capabilities {
		offered = offered || c == family
	}
	if !offered {
		return Command{}, ErrInvalid
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return Command{}, err
	}
	return Command{ID: "central-" + hex.EncodeToString(random), Type: kind, NodeID: node}, nil
}

// Payload is the wire form of the command.
func (c Command) Payload() ([]byte, error) {
	params := map[string]string{}
	if c.NodeID != "" {
		params["node_id"] = c.NodeID
	}
	return json.Marshal(struct {
		Version   int               `json:"version"`
		CommandID string            `json:"command_id"`
		Type      string            `json:"type"`
		Params    map[string]string `json:"params"`
	}{1, c.ID, c.Type, params})
}

// Result is a device's answer to a command.
type Result struct {
	CommandID string  `json:"command_id"`
	Status    string  `json:"status"`
	Reason    *string `json:"reason"`
}

var reasons = map[string]bool{
	"invalid": true, "unsupported": true, "busy": true, "unknown_node": true, "closed": true,
	"not_requested": true, "conflict": true, "full": true, "superseded": true, "storage": true,
	"failed": true,
}

// DecodeResult validates a result: rejected carries a known reason, the others none.
// A reason Central does not know yet is kept as "failed" rather than dropped.
func DecodeResult(payload []byte) (Result, error) {
	var r struct {
		Version int `json:"version"`
		Result
	}
	if len(payload) > MaxStateBytes {
		return Result{}, ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	if err := dec.Decode(&r); err != nil || dec.Decode(new(any)) != io.EOF {
		return Result{}, ErrInvalid
	}
	if r.Version != 1 || !commandID.MatchString(r.CommandID) {
		return Result{}, ErrInvalid
	}
	switch r.Status {
	case "applied", "pending":
		if r.Reason != nil {
			return Result{}, ErrInvalid
		}
	case "rejected":
		if r.Reason == nil || !token.MatchString(*r.Reason) {
			return Result{}, ErrInvalid
		}
		if !reasons[*r.Reason] {
			failed := "failed"
			r.Reason = &failed
		}
	default:
		return Result{}, ErrInvalid
	}
	return r.Result, nil
}

// CommandRecord is a command Central sent and its latest answer. Status "sent" means
// no answer yet; a command still unanswered after CommandTimeout was not delivered.
type CommandRecord struct {
	ID        string    `json:"command_id"`
	SourceID  string    `json:"source_id"`
	DeviceID  string    `json:"device_id"`
	Type      string    `json:"type"`
	NodeID    string    `json:"node_id,omitempty"`
	Status    string    `json:"status"`
	Reason    *string   `json:"reason,omitempty"`
	SentAt    time.Time `json:"sent_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CommandTimeout bounds the wait for a receiver's first answer. A receiver answers within
// seconds or not at all: it drops commands that waited more than 5 s.
const CommandTimeout = 30 * time.Second

// PendingTimeout bounds the wait for a pending command's final answer: the two-minute
// pairing window plus a margin.
const PendingTimeout = 3 * time.Minute
