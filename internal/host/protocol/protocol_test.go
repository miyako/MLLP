package protocol

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// --- Writer tests ---

func TestWriterProducesNDJSON(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	if err := w.Write(map[string]any{"protocol": 1, "kind": "ready"}); err != nil {
		t.Fatal(err)
	}
	line := b.String()
	if !strings.HasSuffix(line, "\n") {
		t.Error("NDJSON line must end with newline")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
}

func TestWriterNoHTMLEscape(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	_ = w.Write(map[string]any{"raw": "a < b > c & d"})
	if strings.Contains(b.String(), `\u003`) {
		t.Error("HTML escaping present in stdout output")
	}
}

// --- Reader tests ---

func frame(kind string, epoch string, seq uint64) string {
	m := map[string]any{
		"protocol": 1,
		"kind":     kind,
		"epoch":    epoch,
		"seq":      seq,
	}
	b, _ := json.Marshal(m)
	return string(b) + "\n"
}

func TestReaderHelloThenCommand(t *testing.T) {
	input := frame("hello", "epoch-abc123", 1) +
		frame("ping", "epoch-abc123", 2)
	r := NewReader(strings.NewReader(input))

	cmd, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Kind != "hello" {
		t.Errorf("first kind: got %q", cmd.Kind)
	}

	cmd, err = r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Kind != "ping" {
		t.Errorf("second kind: got %q", cmd.Kind)
	}
}

func TestReaderCleanEOFAfterHello(t *testing.T) {
	input := frame("hello", "epoch-abc123", 1)
	r := NewReader(strings.NewReader(input))
	if _, err := r.Read(); err != nil {
		t.Fatal(err)
	}
	_, err := r.Read()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestReaderDuplicateHelloRejected(t *testing.T) {
	input := frame("hello", "epoch-abc123", 1) +
		frame("hello", "epoch-abc123", 2)
	r := NewReader(strings.NewReader(input))
	if _, err := r.Read(); err != nil {
		t.Fatal(err)
	}
	_, err := r.Read()
	if err == nil {
		t.Fatal("duplicate hello accepted")
	}
}

func TestReaderHelloWithoutEpochRejected(t *testing.T) {
	line := `{"protocol":1,"kind":"hello","seq":1}` + "\n"
	r := NewReader(strings.NewReader(line))
	_, err := r.Read()
	if err == nil {
		t.Fatal("hello without epoch accepted")
	}
}

func TestReaderCommandBeforeHelloRejected(t *testing.T) {
	input := frame("ping", "epoch-abc123", 1)
	r := NewReader(strings.NewReader(input))
	_, err := r.Read()
	if err == nil {
		t.Fatal("command before hello accepted")
	}
}

func TestReaderEpochMismatchRejected(t *testing.T) {
	input := frame("hello", "epoch-abc123", 1) +
		frame("ping", "epoch-WRONG", 2)
	r := NewReader(strings.NewReader(input))
	if _, err := r.Read(); err != nil {
		t.Fatal(err)
	}
	_, err := r.Read()
	if err == nil {
		t.Fatal("epoch mismatch accepted")
	}
}

func TestReaderSequenceMismatchRejected(t *testing.T) {
	input := frame("hello", "epoch-abc123", 1) +
		frame("ping", "epoch-abc123", 5) // gap: should be 2
	r := NewReader(strings.NewReader(input))
	if _, err := r.Read(); err != nil {
		t.Fatal(err)
	}
	_, err := r.Read()
	if err == nil {
		t.Fatal("sequence gap accepted")
	}
}

func TestReaderWrongProtocolVersion(t *testing.T) {
	line := `{"protocol":2,"kind":"hello","epoch":"e","seq":1}` + "\n"
	r := NewReader(strings.NewReader(line))
	_, err := r.Read()
	if err == nil {
		t.Fatal("wrong protocol version accepted")
	}
}

func TestReaderOversizedFrameRejected(t *testing.T) {
	// Build a line just over the limit.
	large := `{"protocol":1,"kind":"hello","epoch":"e","seq":1,"data":"` +
		strings.Repeat("x", MaxStdioFrame) + `"}` + "\n"
	r := NewReader(strings.NewReader(large))
	_, err := r.Read()
	if err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestReaderMalformedJSONRejected(t *testing.T) {
	r := NewReader(strings.NewReader("{not json}\n"))
	_, err := r.Read()
	if err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestReaderRawMessageAvailable(t *testing.T) {
	line := `{"protocol":1,"kind":"hello","epoch":"epoch-abc123","seq":1,"extra":"value"}` + "\n"
	r := NewReader(strings.NewReader(line))
	cmd, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	if err := json.Unmarshal(cmd.Raw, &full); err != nil {
		t.Fatal(err)
	}
	if full["extra"] != "value" {
		t.Errorf("extra field not in raw: %v", full)
	}
}

func TestValidateID(t *testing.T) {
	for _, tc := range []struct {
		s     string
		valid bool
	}{
		{"msg-0001", true},
		{"conn.0001:A", true},
		{"epoch_abc-123", true},
		{"", false},
		{strings.Repeat("a", 97), false},
		{"has space", false},
		{"has/slash", false},
	} {
		if got := ValidateID(tc.s); got != tc.valid {
			t.Errorf("ValidateID(%q) = %v, want %v", tc.s, got, tc.valid)
		}
	}
}
