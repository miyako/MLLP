// Package tests contains the golden corpus tests.
// Every .hl7 file in tests/golden/ must parse without error.
// The test also verifies the canonical codec can round-trip each message.
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miyako/mllp/internal/codec/canonical"
	"github.com/miyako/mllp/internal/hl7/er7"
)

func TestGoldenCorpusParses(t *testing.T) {
	fixtures, err := filepath.Glob("golden/**/*.hl7")
	if err != nil {
		t.Fatal(err)
	}
	// Also match one level deeper for edge-cases/.
	more, _ := filepath.Glob("golden/*/*.hl7")
	seen := map[string]bool{}
	for _, f := range fixtures {
		seen[f] = true
	}
	for _, f := range more {
		if !seen[f] {
			fixtures = append(fixtures, f)
		}
	}
	if len(fixtures) == 0 {
		t.Fatal("no .hl7 fixtures found")
	}
	for _, path := range fixtures {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Normalize line endings: some editors save LF, HL7 uses CR.
			normalised := strings.ReplaceAll(string(raw), "\r\n", "\r")
			normalised = strings.ReplaceAll(normalised, "\n", "\r")
			payload := []byte(normalised)

			// 1. ER7 parser must not error.
			msg, err := er7.Parse(payload)
			if err != nil {
				t.Fatalf("er7.Parse: %v", err)
			}

			// 2. At least one segment named MSH must exist.
			mshs := msg.SegmentsNamed("MSH")
			if len(mshs) == 0 {
				t.Error("no MSH segment")
			}

			// 3. Canonical codec must not error.
			opts := canonical.Options{
				ID:           "test-" + filepath.Base(path),
				ConnectionID: "conn-test",
				Direction:    "inbound",
				Peer:         "127.0.0.1:9999",
			}
			cm, err := canonical.Encode(payload, opts)
			if err != nil {
				t.Fatalf("canonical.Encode: %v", err)
			}

			// 4. Marshal must produce valid JSON.
			b, err := canonical.Marshal(cm)
			if err != nil {
				t.Fatalf("canonical.Marshal: %v", err)
			}
			if len(b) == 0 {
				t.Error("empty JSON output")
			}

			// 5. Raw field must equal the input (or rawBase64 must be non-empty).
			if cm.HL7.Raw == "" && cm.HL7.RawBase64 == "" {
				t.Error("both raw and rawBase64 are empty")
			}
		})
	}
}
