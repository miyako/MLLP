package ack

import (
	"strings"
	"testing"
	"time"

	"github.com/miyako/mllp/internal/hl7/er7"
)

const adtA01 = "MSH|^~\\&|HIS|CENTRAL|LAB|PATHOLOGY|20261117100000||ADT^A01|MSG00001|P|2.5.1\r" +
	"PID|1||12345||Doe^John\r"

func parseOrFail(t *testing.T, raw string) *er7.Message {
	t.Helper()
	msg, err := er7.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// TestAASwapsSenderReceiver verifies the core field-swapping requirement.
func TestAASwapsSenderReceiver(t *testing.T) {
	orig := parseOrFail(t, adtA01)
	ts := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	ackStr, err := Build(orig, Options{
		Code:         AA,
		ACKControlID: "ACK0001",
		Timestamp:    ts,
	})
	if err != nil {
		t.Fatal(err)
	}

	ack, err := er7.Parse([]byte(ackStr))
	if err != nil {
		t.Fatalf("ACK parse failed: %v\nACK was:\n%s", err, ackStr)
	}

	// ACK MSH-3 (sending app) = original MSH-5 (receiving app of original)
	if ack.SendingApp != "LAB" {
		t.Errorf("ACK sending app: got %q want LAB", ack.SendingApp)
	}
	// ACK MSH-4 (sending fac) = original MSH-6
	if ack.SendingFac != "PATHOLOGY" {
		t.Errorf("ACK sending fac: got %q want PATHOLOGY", ack.SendingFac)
	}
	// ACK MSH-5 (receiving app) = original MSH-3
	if ack.ReceivingApp != "HIS" {
		t.Errorf("ACK receiving app: got %q want HIS", ack.ReceivingApp)
	}
	// ACK MSH-6 (receiving fac) = original MSH-4
	if ack.ReceivingFac != "CENTRAL" {
		t.Errorf("ACK receiving fac: got %q want CENTRAL", ack.ReceivingFac)
	}
}

// TestMSA2EchoesOriginalControlID is the most critical correctness requirement.
func TestMSA2EchoesOriginalControlID(t *testing.T) {
	orig := parseOrFail(t, adtA01)
	ackStr, err := Build(orig, Options{Code: AA, ACKControlID: "ACK0001"})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := er7.Parse([]byte(ackStr))
	if err != nil {
		t.Fatal(err)
	}
	msa2 := ack.FieldAt("MSA", 2)
	if msa2 != "MSG00001" {
		t.Errorf("MSA-2: got %q want MSG00001", msa2)
	}
}

// TestMSA1Code verifies the ACK code is placed in MSA-1.
func TestMSA1Code(t *testing.T) {
	orig := parseOrFail(t, adtA01)
	for _, code := range []Code{AA, AE, AR} {
		ackStr, err := Build(orig, Options{Code: code, ACKControlID: "ACK0001"})
		if err != nil {
			t.Fatalf("%s: build error: %v", code, err)
		}
		ack, err := er7.Parse([]byte(ackStr))
		if err != nil {
			t.Fatalf("%s: parse error: %v", code, err)
		}
		msa1 := ack.FieldAt("MSA", 1)
		if msa1 != string(code) {
			t.Errorf("MSA-1: got %q want %q", msa1, code)
		}
	}
}

// TestACKMessageType verifies MSH-9 is "ACK".
func TestACKMessageType(t *testing.T) {
	orig := parseOrFail(t, adtA01)
	ackStr, err := Build(orig, Options{Code: AA, ACKControlID: "ACK0001"})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := er7.Parse([]byte(ackStr))
	if err != nil {
		t.Fatal(err)
	}
	if ack.MessageType != "ACK" {
		t.Errorf("ACK message type: got %q want ACK", ack.MessageType)
	}
}

// TestVersionPreserved verifies the original HL7 version is kept.
func TestVersionPreserved(t *testing.T) {
	orig := parseOrFail(t, adtA01)
	ackStr, err := Build(orig, Options{Code: AA, ACKControlID: "ACK0001"})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := er7.Parse([]byte(ackStr))
	if err != nil {
		t.Fatal(err)
	}
	if ack.Version != "2.5.1" {
		t.Errorf("ACK version: got %q want 2.5.1", ack.Version)
	}
}

// TestAEHasERRSegment verifies AE responses include an ERR segment when
// an error code is provided.
func TestAEHasERRSegment(t *testing.T) {
	orig := parseOrFail(t, adtA01)
	ackStr, err := Build(orig, Options{
		Code:         AE,
		ACKControlID: "ACK0001",
		ErrorCode:    "207",
		ErrorText:    "application error",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ackStr, "ERR") {
		t.Errorf("AE ACK missing ERR segment:\n%s", ackStr)
	}
	ack, err := er7.Parse([]byte(ackStr))
	if err != nil {
		t.Fatal(err)
	}
	errs := ack.SegmentsNamed("ERR")
	if len(errs) == 0 {
		t.Fatal("ERR segment not parsed")
	}
}

// TestAANoERRSegment verifies AA responses do not include an ERR segment.
func TestAANoERRSegment(t *testing.T) {
	orig := parseOrFail(t, adtA01)
	ackStr, err := Build(orig, Options{Code: AA, ACKControlID: "ACK0001"})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := er7.Parse([]byte(ackStr))
	if err != nil {
		t.Fatal(err)
	}
	if errs := ack.SegmentsNamed("ERR"); len(errs) != 0 {
		t.Error("AA ACK should not contain ERR segment")
	}
}

// TestBuildNoControlID rejects messages without MSH-10.
func TestBuildNoControlID(t *testing.T) {
	raw := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01||P|2.5.1\r" // empty MSH-10
	orig := parseOrFail(t, raw)
	_, err := Build(orig, Options{Code: AA})
	if err != ErrNoControlID {
		t.Errorf("expected ErrNoControlID, got %v", err)
	}
}

// TestBuildInvalidCode rejects unknown ACK codes.
func TestBuildInvalidCode(t *testing.T) {
	orig := parseOrFail(t, adtA01)
	_, err := Build(orig, Options{Code: "XX"})
	if err != ErrInvalidCode {
		t.Errorf("expected ErrInvalidCode, got %v", err)
	}
}

// TestValidateCorrelation verifies ACK correlation checking.
func TestValidateCorrelation(t *testing.T) {
	orig := parseOrFail(t, adtA01)
	ackStr, err := Build(orig, Options{Code: AA, ACKControlID: "ACK0001"})
	if err != nil {
		t.Fatal(err)
	}
	ackMsg, err := er7.Parse([]byte(ackStr))
	if err != nil {
		t.Fatal(err)
	}

	// Correct correlation.
	if err := Validate("MSG00001", ackMsg); err != nil {
		t.Errorf("correct correlation failed: %v", err)
	}

	// Wrong original control ID.
	if err := Validate("WRONG-ID", ackMsg); err == nil {
		t.Error("mismatched correlation accepted")
	}
}

// TestCodeFromMSA1 verifies extraction of ACK codes from received messages.
func TestCodeFromMSA1(t *testing.T) {
	for _, code := range []Code{AA, AE, AR} {
		orig := parseOrFail(t, adtA01)
		ackStr, err := Build(orig, Options{Code: code, ACKControlID: "ACK0001"})
		if err != nil {
			t.Fatal(err)
		}
		ackMsg, err := er7.Parse([]byte(ackStr))
		if err != nil {
			t.Fatal(err)
		}
		got, err := CodeFromMSA1(ackMsg)
		if err != nil {
			t.Fatalf("%s: CodeFromMSA1 failed: %v", code, err)
		}
		if got != code {
			t.Errorf("code: got %q want %q", got, code)
		}
	}
}

// TestSendingAppOverride verifies the caller can override sender identity.
func TestSendingAppOverride(t *testing.T) {
	orig := parseOrFail(t, adtA01)
	ackStr, err := Build(orig, Options{
		Code:         AA,
		ACKControlID: "ACK0001",
		SendingApp:   "CUSTOM_APP",
		SendingFac:   "CUSTOM_FAC",
	})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := er7.Parse([]byte(ackStr))
	if err != nil {
		t.Fatal(err)
	}
	if ack.SendingApp != "CUSTOM_APP" {
		t.Errorf("override sending app: got %q", ack.SendingApp)
	}
	if ack.SendingFac != "CUSTOM_FAC" {
		t.Errorf("override sending fac: got %q", ack.SendingFac)
	}
}
