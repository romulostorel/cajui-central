// Package devicestate decodes the retained device state and availability that
// cajui-firmware receivers publish on their management channel
// (cajui-firmware docs/management-v1.md). State describes a device; it is not a
// measurement and never enters telemetry history.
package devicestate

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxStateBytes bounds one state message; the firmware's buffer is 1024 bytes.
const MaxStateBytes = 4096

var ErrInvalid = errors.New("invalid device state")

var (
	sourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	deviceID = regexp.MustCompile(`^[0-9a-f]{16}$`)
	token    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	version  = regexp.MustCompile(`^[0-9]{1,5}\.[0-9]{1,5}\.[0-9]{1,5}$`)
)

// State keeps the fields Central understands. Unknown fields are ignored, as the
// contract requires, and absent or null values stay nil: unknown, never zero.
type State struct {
	Version      int         `json:"version"`
	SourceID     string      `json:"source_id"`
	DeviceID     string      `json:"device_id"`
	Role         string      `json:"role"`
	Model        *string     `json:"model,omitempty"`
	Firmware     *Firmware   `json:"firmware,omitempty"`
	Radio        *Radio      `json:"radio,omitempty"`
	UptimeS      *int64      `json:"uptime_s,omitempty"`
	ResetReason  *string     `json:"reset_reason,omitempty"`
	WiFi         *WiFi       `json:"wifi,omitempty"`
	Queue        *Queue      `json:"queue,omitempty"`
	Forwarding   *Forwarding `json:"forwarding,omitempty"`
	Pairing      *Pairing    `json:"pairing,omitempty"`
	Capabilities []string    `json:"capabilities,omitempty"`
	ReceiverID   *string     `json:"receiver_id,omitempty"`
	Binding      *string     `json:"binding,omitempty"`
	LastFrame    *Frame      `json:"last_frame,omitempty"`
	Parameters   *Parameters `json:"parameters,omitempty"`
}
type Firmware struct {
	Version *string `json:"version,omitempty"`
	Slot    *string `json:"slot,omitempty"`
	State   *string `json:"state,omitempty"`
}
type Radio struct {
	Profile  *int64 `json:"profile,omitempty"`
	PowerDBm *int64 `json:"power_dbm,omitempty"`
}
type WiFi struct {
	RSSIDBm *int64 `json:"rssi_dbm,omitempty"`
}
type Queue struct {
	Depth    *int64 `json:"depth,omitempty"`
	Capacity *int64 `json:"capacity,omitempty"`
}
type Forwarding struct {
	Published *int64 `json:"published,omitempty"`
	Retries   *int64 `json:"retries,omitempty"`
}
type Pairing struct {
	Open       bool      `json:"open"`
	RemainingS *int64    `json:"remaining_s,omitempty"`
	Requests   []Request `json:"requests"`
}
type Request struct {
	NodeID   string `json:"node_id"`
	RSSIDBm  *int64 `json:"rssi_dbm,omitempty"`
	Conflict bool   `json:"conflict"`
}
type Frame struct {
	Counter         *int64   `json:"counter,omitempty"`
	RSSIDBm         *int64   `json:"rssi_dbm,omitempty"`
	SNRDB           *float64 `json:"snr_db,omitempty"`
	ReceiverUptimeS *int64   `json:"receiver_uptime_s,omitempty"`
}
type Parameters struct {
	IntervalS *int64 `json:"interval_s,omitempty"`
	PowerDBm  *int64 `json:"power_dbm,omitempty"`
}

// Stored is the latest state and availability of one device as Central received it.
// Retained marks a broker snapshot of unknown age that differed from what was stored.
type Stored struct {
	State
	ReceivedAt     time.Time  `json:"received_at"`
	Retained       bool       `json:"retained"`
	Availability   *string    `json:"availability,omitempty"`
	AvailabilityAt *time.Time `json:"availability_at,omitempty"`
}

// Topic is the management topic of one device (kind "state" or "availability").
func Topic(source, device, kind string) string {
	return "manage/v1/" + source + "/" + device + "/" + kind
}

// ParseTopic returns the identities and kind of a management topic Central reads.
func ParseTopic(topic string) (source, device, kind string, ok bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 5 || parts[0] != "manage" || parts[1] != "v1" || !sourceID.MatchString(parts[2]) || !deviceID.MatchString(parts[3]) {
		return "", "", "", false
	}
	if parts[4] != "state" && parts[4] != "availability" {
		return "", "", "", false
	}
	return parts[2], parts[3], parts[4], true
}

// DecodeAvailability accepts exactly the two payloads of the contract.
func DecodeAvailability(payload []byte) (string, error) {
	switch string(payload) {
	case "online", "offline":
		return string(payload), nil
	}
	return "", ErrInvalid
}

// Decode validates a state published on the topic of source/device.
func Decode(source, device string, payload []byte) (State, error) {
	var s State
	if len(payload) > MaxStateBytes || !utf8.Valid(payload) {
		return s, ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	if err := dec.Decode(&s); err != nil {
		return State{}, ErrInvalid
	}
	if dec.More() {
		return State{}, ErrInvalid
	}
	if s.Version != 1 || s.SourceID != source || s.DeviceID != device || !sourceID.MatchString(source) || !deviceID.MatchString(device) {
		return State{}, ErrInvalid
	}
	if err := s.validate(); err != nil {
		return State{}, err
	}
	return s, nil
}

func (s State) validate() error {
	ok := true
	text := func(value *string, pattern *regexp.Regexp) {
		if value != nil && !pattern.MatchString(*value) {
			ok = false
		}
	}
	number := func(value *int64, low, high int64) {
		if value != nil && (*value < low || *value > high) {
			ok = false
		}
	}
	switch s.Role {
	case "receiver":
		if s.ReceiverID != nil || s.Binding != nil || s.LastFrame != nil || s.Parameters != nil {
			return ErrInvalid
		}
	case "transmitter":
		if s.ReceiverID == nil || !deviceID.MatchString(*s.ReceiverID) || s.Binding == nil ||
			(*s.Binding != "active" && *s.Binding != "pending" && *s.Binding != "revoked") ||
			s.Pairing != nil || s.Queue != nil || s.Forwarding != nil || s.WiFi != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if s.Model != nil && !printable(*s.Model) {
		return ErrInvalid
	}
	if s.Firmware != nil {
		text(s.Firmware.Version, version)
		text(s.Firmware.Slot, token)
		text(s.Firmware.State, token)
	}
	text(s.ResetReason, token)
	number(s.UptimeS, 0, math.MaxUint32)
	if s.Radio != nil {
		number(s.Radio.Profile, 0, math.MaxUint16)
		number(s.Radio.PowerDBm, -128, 127)
	}
	if s.WiFi != nil {
		number(s.WiFi.RSSIDBm, -200, 0)
	}
	if s.Queue != nil {
		number(s.Queue.Depth, 0, 1<<20)
		number(s.Queue.Capacity, 0, 1<<20)
	}
	if s.Forwarding != nil {
		number(s.Forwarding.Published, 0, math.MaxUint32)
		number(s.Forwarding.Retries, 0, math.MaxUint32)
	}
	if s.Pairing != nil {
		number(s.Pairing.RemainingS, 0, 86400)
		if len(s.Pairing.Requests) > 16 {
			return ErrInvalid
		}
		for _, r := range s.Pairing.Requests {
			if !deviceID.MatchString(r.NodeID) {
				return ErrInvalid
			}
			number(r.RSSIDBm, -200, 0)
		}
	}
	if len(s.Capabilities) > 16 {
		return ErrInvalid
	}
	for _, c := range s.Capabilities {
		text(&c, token)
	}
	if s.LastFrame != nil {
		number(s.LastFrame.Counter, 0, math.MaxInt64)
		number(s.LastFrame.RSSIDBm, -200, 0)
		number(s.LastFrame.ReceiverUptimeS, 0, math.MaxUint32)
		if s.LastFrame.SNRDB != nil && (math.IsNaN(*s.LastFrame.SNRDB) || *s.LastFrame.SNRDB < -50 || *s.LastFrame.SNRDB > 50) {
			return ErrInvalid
		}
	}
	if s.Parameters != nil {
		number(s.Parameters.IntervalS, 1, 604800)
		number(s.Parameters.PowerDBm, -128, 127)
	}
	if !ok {
		return ErrInvalid
	}
	return nil
}

// printable accepts short display text from firmware: no control characters.
func printable(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= 64 && !strings.ContainsFunc(value, unicode.IsControl)
}
