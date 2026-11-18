// Package canonical implements the canonical JSON representation of HL7 v2 messages.
//
// The schema is Protocol 1 as defined in docs/protocol/protocol-1.md.
// Key invariants:
//   - raw is authoritative; segments is a derived parsed view.
//   - Non-UTF-8 payloads are base64-encoded with explicit charset metadata.
//   - Escape sequences, empty fields, repetitions, Z-segments are all preserved.
//   - Numeric identifiers remain strings (no silent type coercion).
package canonical

import (
	"encoding/base64"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/miyako/mllp/internal/hl7/er7"
)

// Transport carries connection metadata for a message.
type Transport struct {
	Peer string `json:"peer"`
	TLS  bool   `json:"tls"`
}

// SeparatorJSON is the JSON form of the HL7 encoding characters.
type SeparatorJSON struct {
	Field        string `json:"field"`
	Component    string `json:"component"`
	Repetition   string `json:"repetition"`
	Escape       string `json:"escape"`
	Subcomponent string `json:"subcomponent"`
}

// SegmentJSON is one segment in the canonical representation.
type SegmentJSON struct {
	Name   string   `json:"name"`
	Fields []string `json:"fields"`
}

// HL7JSON is the hl7 object inside a canonical message.
type HL7JSON struct {
	Encoding    string        `json:"encoding"`
	Version     string        `json:"version,omitempty"`
	Charset     string        `json:"charset,omitempty"`
	MessageType string        `json:"messageType,omitempty"`
	ControlID   string        `json:"controlId,omitempty"`
	Separators  SeparatorJSON `json:"separators"`
	// Raw is the original ER7 payload.
	// Present as a string when the payload is valid UTF-8.
	Raw string `json:"raw,omitempty"`
	// RawBase64 is set instead of Raw when the payload is not valid UTF-8.
	RawBase64 string `json:"rawBase64,omitempty"`
	Segments  []SegmentJSON `json:"segments"`
}

// Message is the top-level canonical JSON object for an inbound HL7 message.
type Message struct {
	Protocol     int       `json:"protocol"`
	Kind         string    `json:"kind"`
	ID           string    `json:"id"`
	ConnectionID string    `json:"connectionId"`
	Direction    string    `json:"direction"`
	ReceivedAt   string    `json:"receivedAt"`
	Transport    Transport `json:"transport"`
	HL7          HL7JSON   `json:"hl7"`
}

// Options controls message conversion behaviour.
type Options struct {
	// ID is the unique message identifier (distinct from MSH-10 ControlID).
	ID string
	// ConnectionID identifies the TCP connection that delivered the message.
	ConnectionID string
	// Direction is "inbound" or "outbound".
	Direction string
	// ReceivedAt is the timestamp of receipt. Zero means time.Now().
	ReceivedAt time.Time
	// Peer is the remote address string (e.g. "192.0.2.1:2575").
	Peer string
	// TLS indicates whether the connection uses TLS.
	TLS bool
	// CharsetOverride allows a partner-profile to specify the expected charset
	// when MSH-18 is absent or untrustworthy.
	CharsetOverride string
}

// Encode converts a raw HL7 ER7 payload into the canonical JSON Message.
// The raw bytes are stored as-is (or base64 for non-UTF-8 payloads).
func Encode(payload []byte, opts Options) (*Message, error) {
	msg, err := er7.Parse(payload)
	if err != nil {
		return nil, err
	}

	if opts.ReceivedAt.IsZero() {
		opts.ReceivedAt = time.Now()
	}

	charset := msg.CharacterSet
	if opts.CharsetOverride != "" {
		charset = opts.CharsetOverride
	}

	sep := msg.Separators
	hl7 := HL7JSON{
		Encoding:    "ER7",
		Version:     msg.Version,
		Charset:     charset,
		MessageType: msg.MessageType,
		ControlID:   msg.ControlID,
		Separators: SeparatorJSON{
			Field:        string(sep.Field),
			Component:    string(sep.Component),
			Repetition:   string(sep.Repetition),
			Escape:       string(sep.Escape),
			Subcomponent: string(sep.Subcomponent),
		},
	}

	// Store raw payload. If not valid UTF-8, use base64 and flag it.
	if utf8.Valid(payload) {
		hl7.Raw = string(payload)
	} else {
		hl7.RawBase64 = base64.StdEncoding.EncodeToString(payload)
	}

	// Convert parsed segments.
	for _, seg := range msg.Segments {
		hl7.Segments = append(hl7.Segments, SegmentJSON{
			Name:   seg.Name,
			Fields: seg.Fields,
		})
	}

	direction := opts.Direction
	if direction == "" {
		direction = "inbound"
	}

	return &Message{
		Protocol:     1,
		Kind:         "message",
		ID:           opts.ID,
		ConnectionID: opts.ConnectionID,
		Direction:    direction,
		ReceivedAt:   opts.ReceivedAt.Format(time.RFC3339Nano),
		Transport: Transport{
			Peer: opts.Peer,
			TLS:  opts.TLS,
		},
		HL7: hl7,
	}, nil
}

// Marshal serialises a canonical Message to a JSON byte slice.
// HTML escaping is disabled so HL7 characters like < > & survive intact.
func Marshal(m *Message) ([]byte, error) {
	buf, err := marshalNoEscape(m)
	return buf, err
}

func marshalNoEscape(v any) ([]byte, error) {
	// json.Marshal escapes HTML by default. We need to disable that.
	// Use a bytes.Buffer + json.NewEncoder which lets us call SetEscapeHTML(false).
	var buf jsonBuffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encoder appends a newline; trim it so callers control line endings.
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

type jsonBuffer struct {
	b []byte
}

func (j *jsonBuffer) Write(p []byte) (int, error) {
	j.b = append(j.b, p...)
	return len(p), nil
}

func (j *jsonBuffer) Bytes() []byte { return j.b }
