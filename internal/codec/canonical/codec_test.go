package canonical

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const adtA01 = "MSH|^~\\&|HIS|CENTRAL|LAB|PATHOLOGY|20261117100000||ADT^A01|MSG00001|P|2.5.1|||AL|NE||8859/1\r" +
	"EVN|A01|20261117100000\r" +
	"PID|1||12345^^^MRN||Doe^John^A||19800101|M\r" +
	"PV1|1|I|CCU^101^A\r"

func opts(id string) Options {
	return Options{
		ID:           id,
		ConnectionID: "conn-0001",
		Direction:    "inbound",
		ReceivedAt:   time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
		Peer:         "127.0.0.1:2575",
		TLS:          true,
	}
}

func TestEncodeBasicFields(t *testing.T) {
	m, err := Encode([]byte(adtA01), opts("msg-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Protocol != 1 {
		t.Errorf("protocol: got %d", m.Protocol)
	}
	if m.Kind != "message" {
		t.Errorf("kind: got %q", m.Kind)
	}
	if m.ID != "msg-0001" {
		t.Errorf("id: got %q", m.ID)
	}
	if m.ConnectionID != "conn-0001" {
		t.Errorf("connectionId: got %q", m.ConnectionID)
	}
	if m.Direction != "inbound" {
		t.Errorf("direction: got %q", m.Direction)
	}
	if !m.Transport.TLS {
		t.Error("tls should be true")
	}
	if m.Transport.Peer != "127.0.0.1:2575" {
		t.Errorf("peer: got %q", m.Transport.Peer)
	}
}

func TestEncodeHL7Fields(t *testing.T) {
	m, err := Encode([]byte(adtA01), opts("msg-0001"))
	if err != nil {
		t.Fatal(err)
	}
	hl7 := m.HL7
	if hl7.Encoding != "ER7" {
		t.Errorf("encoding: got %q", hl7.Encoding)
	}
	if hl7.Version != "2.5.1" {
		t.Errorf("version: got %q", hl7.Version)
	}
	if hl7.MessageType != "ADT^A01" {
		t.Errorf("messageType: got %q", hl7.MessageType)
	}
	if hl7.ControlID != "MSG00001" {
		t.Errorf("controlId: got %q", hl7.ControlID)
	}
	if hl7.Charset != "8859/1" {
		t.Errorf("charset: got %q", hl7.Charset)
	}
}

func TestEncodeSeparators(t *testing.T) {
	m, err := Encode([]byte(adtA01), opts("msg-0001"))
	if err != nil {
		t.Fatal(err)
	}
	sep := m.HL7.Separators
	if sep.Field != "|" || sep.Component != "^" || sep.Repetition != "~" ||
		sep.Escape != `\` || sep.Subcomponent != "&" {
		t.Errorf("separators: %+v", sep)
	}
}

func TestEncodeRawIsAuthoritative(t *testing.T) {
	m, err := Encode([]byte(adtA01), opts("msg-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if m.HL7.Raw != adtA01 {
		t.Errorf("raw not preserved:\ngot:  %q\nwant: %q", m.HL7.Raw, adtA01)
	}
	if m.HL7.RawBase64 != "" {
		t.Error("rawBase64 should not be set for valid UTF-8")
	}
}

func TestEncodeSegmentCount(t *testing.T) {
	m, err := Encode([]byte(adtA01), opts("msg-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.HL7.Segments) != 4 {
		t.Errorf("segment count: got %d", len(m.HL7.Segments))
	}
	names := []string{"MSH", "EVN", "PID", "PV1"}
	for i, want := range names {
		if m.HL7.Segments[i].Name != want {
			t.Errorf("segment %d: got %q want %q", i, m.HL7.Segments[i].Name, want)
		}
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	m, err := Encode([]byte(adtA01), opts("msg-0001"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	// Verify the JSON is valid and contains key fields.
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("marshal produced invalid JSON: %v", err)
	}
	if back["kind"] != "message" {
		t.Errorf("kind in JSON: %v", back["kind"])
	}
}

func TestMarshalNoHTMLEscaping(t *testing.T) {
	// HL7 uses < > & characters in some contexts; they must not be escaped.
	raw := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\r" +
		"NTE|1||Note with <angle> & 'quotes'\r"
	m, err := Encode([]byte(raw), opts("msg-html"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, `\u003c`) || strings.Contains(s, `\u003e`) || strings.Contains(s, `\u0026`) {
		t.Errorf("HTML escaping found in output: %s", s)
	}
}

func TestNonUTF8UsesBase64(t *testing.T) {
	// A minimal valid MSH followed by an invalid UTF-8 byte sequence in a field.
	// We build the raw bytes directly to avoid Go string encoding.
	prefix := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\r" +
		"NTE|1||"
	suffix := "\r"
	payload := []byte(prefix)
	payload = append(payload, 0x80, 0x81) // invalid UTF-8
	payload = append(payload, []byte(suffix)...)

	m, err := Encode(payload, opts("msg-latin"))
	if err != nil {
		t.Fatal(err)
	}
	if m.HL7.Raw != "" {
		t.Error("Raw should be empty for non-UTF-8 payload")
	}
	if m.HL7.RawBase64 == "" {
		t.Error("RawBase64 should be set for non-UTF-8 payload")
	}
}

func TestCharsetOverride(t *testing.T) {
	o := opts("msg-override")
	o.CharsetOverride = "Shift_JIS"
	m, err := Encode([]byte(adtA01), o)
	if err != nil {
		t.Fatal(err)
	}
	if m.HL7.Charset != "Shift_JIS" {
		t.Errorf("charset override: got %q", m.HL7.Charset)
	}
}

func TestReceivedAtDefaultsToNow(t *testing.T) {
	before := time.Now()
	o := Options{ID: "x", ConnectionID: "c", Direction: "inbound"}
	m, err := Encode([]byte(adtA01), o)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	t1, err := time.Parse(time.RFC3339Nano, m.ReceivedAt)
	if err != nil {
		t.Fatalf("bad timestamp: %v", err)
	}
	if t1.Before(before) || t1.After(after) {
		t.Errorf("ReceivedAt %v outside [%v, %v]", t1, before, after)
	}
}
