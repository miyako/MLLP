package er7

import (
	"strings"
	"testing"
)

// adtA01 is a minimal but realistic ADT^A01 message.
const adtA01 = "MSH|^~\\&|HIS|CENTRAL|LAB|PATHOLOGY|20261117100000||ADT^A01|MSG00001|P|2.5.1|||AL|NE||8859/1\r" +
	"EVN|A01|20261117100000\r" +
	"PID|1||12345^^^MRN||Doe^John^A||19800101|M\r" +
	"PV1|1|I|CCU^101^A\r"

func TestParseSeparators(t *testing.T) {
	msg, err := Parse([]byte(adtA01))
	if err != nil {
		t.Fatal(err)
	}
	sep := msg.Separators
	if sep.Field != '|' || sep.Component != '^' || sep.Repetition != '~' ||
		sep.Escape != '\\' || sep.Subcomponent != '&' {
		t.Fatalf("unexpected separators: %+v", sep)
	}
}

func TestParseMSHMetadata(t *testing.T) {
	msg, err := Parse([]byte(adtA01))
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"SendingApp":    "HIS",
		"SendingFac":    "CENTRAL",
		"ReceivingApp":  "LAB",
		"ReceivingFac":  "PATHOLOGY",
		"MessageType":   "ADT^A01",
		"ControlID":     "MSG00001",
		"ProcessingID":  "P",
		"Version":       "2.5.1",
		"AcceptACKType": "AL",
		"AppACKType":    "NE",
		"CharacterSet":  "8859/1",
	}
	for field, want := range checks {
		var got string
		switch field {
		case "SendingApp":
			got = msg.SendingApp
		case "SendingFac":
			got = msg.SendingFac
		case "ReceivingApp":
			got = msg.ReceivingApp
		case "ReceivingFac":
			got = msg.ReceivingFac
		case "MessageType":
			got = msg.MessageType
		case "ControlID":
			got = msg.ControlID
		case "ProcessingID":
			got = msg.ProcessingID
		case "Version":
			got = msg.Version
		case "AcceptACKType":
			got = msg.AcceptACKType
		case "AppACKType":
			got = msg.AppACKType
		case "CharacterSet":
			got = msg.CharacterSet
		}
		if got != want {
			t.Errorf("%s: got %q want %q", field, got, want)
		}
	}
}

func TestParseSegmentCount(t *testing.T) {
	msg, err := Parse([]byte(adtA01))
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Segments) != 4 {
		t.Fatalf("expected 4 segments, got %d: %v", len(msg.Segments), segNames(msg))
	}
}

func segNames(msg *Message) []string {
	var names []string
	for _, s := range msg.Segments {
		names = append(names, s.Name)
	}
	return names
}

func TestEmptyFieldsPreserved(t *testing.T) {
	// PID with several empty fields to verify preservation.
	raw := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\r" +
		"PID|1|||12345||Doe^John\r" // fields 2 and 3 are empty
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	pid := msg.SegmentsNamed("PID")
	if len(pid) == 0 {
		t.Fatal("no PID segment")
	}
	// Fields[0]=PID-1="1", Fields[1]=PID-2="", Fields[2]=PID-3=""
	if len(pid[0].Fields) < 3 {
		t.Fatalf("empty fields collapsed: %v", pid[0].Fields)
	}
	if pid[0].Fields[1] != "" || pid[0].Fields[2] != "" {
		t.Errorf("empty fields not preserved: %v", pid[0].Fields)
	}
	if pid[0].Fields[3] != "12345" {
		t.Errorf("field 4 wrong: %q", pid[0].Fields[3])
	}
}

func TestZSegmentPreserved(t *testing.T) {
	raw := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\r" +
		"ZPD|extra|partner-specific-data\r"
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	zpd := msg.SegmentsNamed("ZPD")
	if len(zpd) == 0 {
		t.Fatal("Z-segment not preserved")
	}
	if zpd[0].Fields[1] != "partner-specific-data" {
		t.Errorf("Z-segment field wrong: %v", zpd[0].Fields)
	}
}

func TestRepetitionsPreserved(t *testing.T) {
	// PID-3 with two repetitions.
	raw := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\r" +
		"PID|1||12345^^^MRN~67890^^^SS||Doe^John\r"
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	pid := msg.SegmentsNamed("PID")
	// Fields[2] = PID-3 raw string, should contain "~".
	if !strings.Contains(pid[0].Fields[2], "~") {
		t.Errorf("repetition separator lost in: %q", pid[0].Fields[2])
	}
}

func TestEscapeSequencesPreserved(t *testing.T) {
	// A field containing an escape sequence \F\ (escaped field sep).
	raw := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\r" +
		"NTE|1||Comment with \\F\\ escape\r"
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	nte := msg.SegmentsNamed("NTE")
	if len(nte) == 0 {
		t.Fatal("NTE not found")
	}
	if !strings.Contains(nte[0].Fields[2], `\F\`) {
		t.Errorf("escape sequence lost: %q", nte[0].Fields[2])
	}
}

func TestCustomSeparators(t *testing.T) {
	// Use non-standard separators.
	raw := "MSH$+-!@|custom$message$type|ID2|P|2.5\r"
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	sep := msg.Separators
	if sep.Field != '$' || sep.Component != '+' || sep.Repetition != '-' ||
		sep.Escape != '!' || sep.Subcomponent != '@' {
		t.Fatalf("custom separators wrong: %+v", sep)
	}
}

func TestFieldAt(t *testing.T) {
	msg, err := Parse([]byte(adtA01))
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.FieldAt("MSH", 9); got != "ADT^A01" {
		t.Errorf("MSH-9: got %q", got)
	}
	if got := msg.FieldAt("PID", 5); got != "Doe^John^A" {
		t.Errorf("PID-5: got %q", got)
	}
	if got := msg.FieldAt("ZZZ", 1); got != "" {
		t.Errorf("missing segment should return empty, got %q", got)
	}
}

func TestOruR01(t *testing.T) {
	raw := "MSH|^~\\&|LAB|PATH|HIS|CENTRAL|20261117110000||ORU^R01|RES0001|P|2.5.1\r" +
		"PID|1||99999||Smith^Jane\r" +
		"OBR|1||LAB001|CBC^Complete Blood Count\r" +
		"OBX|1|NM|WBC^White Blood Cell Count||7.5|10*3/uL|4.5-11.0|N|||F\r"
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if msg.MessageType != "ORU^R01" {
		t.Errorf("message type: got %q", msg.MessageType)
	}
	obx := msg.SegmentsNamed("OBX")
	if len(obx) == 0 {
		t.Fatal("no OBX segment")
	}
	// OBX-5 (index 4) = observed value "7.5"
	if obx[0].Fields[4] != "7.5" {
		t.Errorf("OBX-5: got %q", obx[0].Fields[4])
	}
}

func TestNotHL7(t *testing.T) {
	_, err := Parse([]byte("BHS|^~\\&|..."))
	if err != ErrNotHL7 {
		t.Errorf("expected ErrNotHL7, got %v", err)
	}
}

func TestTooShort(t *testing.T) {
	_, err := Parse([]byte("MSH|"))
	if err == nil {
		t.Error("expected error for short MSH")
	}
}

func TestTrailingCR(t *testing.T) {
	// Message with extra trailing CR — common in real deployments.
	raw := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\r\r"
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if msg.MessageType != "ADT^A01" {
		t.Errorf("message type: got %q", msg.MessageType)
	}
}
