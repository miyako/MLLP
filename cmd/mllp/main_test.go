package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"version"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSuffix(out.Bytes(), []byte("\n")), &m); err != nil {
		t.Fatalf("invalid JSON: %v — output: %s", err, out.String())
	}
	if m["version"] != version {
		t.Errorf("version: got %v want %q", m["version"], version)
	}
	if m["protocol"] != float64(1) {
		t.Errorf("protocol: got %v", m["protocol"])
	}
}

func TestVersionFlag(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"--version"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), version) {
		t.Errorf("version flag output missing version: %s", out.String())
	}
}

func TestNoArgs(t *testing.T) {
	if err := run(nil, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("expected error with no arguments")
	}
}

func TestUnknownCommand(t *testing.T) {
	if err := run([]string{"unknown"}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestConvertFromFile(t *testing.T) {
	// Write a temporary HL7 file.
	content := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\rPID|1||12345||Doe^John\r"
	f, err := os.CreateTemp("", "mllp-*.hl7")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var out bytes.Buffer
	if err := run([]string{"convert", "--from", "er7", "--to", "json", "--file", f.Name()},
		nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSuffix(out.Bytes(), []byte("\n")), &m); err != nil {
		t.Fatalf("convert produced invalid JSON: %v — %s", err, out.String())
	}
	if m["kind"] != "message" {
		t.Errorf("kind: got %v", m["kind"])
	}
}

func TestConvertFromStdin(t *testing.T) {
	content := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\rPID|1||12345||Doe^John\r"
	var out bytes.Buffer
	if err := run([]string{"convert", "--from", "er7", "--to", "json"},
		strings.NewReader(content), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSuffix(out.Bytes(), []byte("\n")), &m); err != nil {
		t.Fatalf("convert produced invalid JSON: %v", err)
	}
	hl7, _ := m["hl7"].(map[string]any)
	if hl7 == nil || hl7["messageType"] != "ADT^A01" {
		t.Errorf("unexpected hl7: %v", m["hl7"])
	}
}

func TestConvertMissingFromTo(t *testing.T) {
	if err := run([]string{"convert"}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("expected error for missing --from and --to")
	}
}

func TestConvertUnsupportedFormat(t *testing.T) {
	if err := run([]string{"convert", "--from", "xml", "--to", "json"}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}

func TestConvertNoHTMLEscape(t *testing.T) {
	content := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\rNTE|1||<b>bold</b> & more\r"
	var out bytes.Buffer
	if err := run([]string{"convert", "--from", "er7", "--to", "json"},
		strings.NewReader(content), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `\u003c`) {
		t.Error("HTML escape found in convert output")
	}
}
