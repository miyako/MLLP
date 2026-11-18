// Package er7 implements a lossless HL7 v2 ER7 parser.
//
// Design principles:
//   - The raw ER7 bytes are authoritative. The parsed representation is derived.
//   - Every field, component, repetition, subcomponent and escape sequence is
//     preserved. Nothing is silently discarded.
//   - Z-segments are preserved as opaque strings.
//   - MSH-1 and MSH-2 are always read dynamically from the message.
//   - Empty fields are preserved (not collapsed).
//   - Numeric identifiers remain strings throughout.
//
// This package must have no knowledge of TCP, JSON, or MLLP framing.
package er7

import (
	"errors"
	"fmt"
	"strings"
)

// Separators holds the five HL7 v2 encoding characters extracted from MSH.
type Separators struct {
	Field        byte // MSH-1, usually '|'
	Component    byte // MSH-2[0], usually '^'
	Repetition   byte // MSH-2[1], usually '~'
	Escape       byte // MSH-2[2], usually '\'
	Subcomponent byte // MSH-2[3], usually '&'
}

// DefaultSeparators are the conventional HL7 v2 encoding characters.
var DefaultSeparators = Separators{
	Field:        '|',
	Component:    '^',
	Repetition:   '~',
	Escape:       '\\',
	Subcomponent: '&',
}

// Field represents one HL7 field value.
// A field may have multiple repetitions; each repetition may have multiple
// components; each component may have multiple subcomponents.
// All values are raw strings — escape sequences are preserved as-is.
type Field struct {
	// Repetitions holds one entry per repetition (~-delimited).
	// A field with no repetition separator has exactly one entry.
	Repetitions [][]string // [repetition][component] — subcomponents are stored joined
	// Raw is the original field string before any splitting.
	Raw string
}

// Segment represents one HL7 v2 segment line.
type Segment struct {
	// Name is the three-character segment name (e.g. "MSH", "PID", "OBX").
	Name string
	// Fields holds the field values starting at field index 1.
	// Fields[0] is field 1 (MSH-1 is the separator character itself).
	// For MSH, Fields[0] is the Field separator string "|",
	// Fields[1] is the encoding characters "^~\&".
	Fields []string
}

// Message is the result of parsing one HL7 v2 ER7 message.
type Message struct {
	Separators Separators
	// Segments in document order, including Z-segments.
	Segments []Segment
	// MSH metadata extracted for convenience (all also available via Segments).
	Version     string // MSH-12
	MessageType string // MSH-9 (e.g. "ADT^A01")
	ControlID   string // MSH-10
	ProcessingID string // MSH-11
	SendingApp  string // MSH-3
	SendingFac  string // MSH-4
	ReceivingApp string // MSH-5
	ReceivingFac string // MSH-6
	DateTime    string // MSH-7
	// ACKMode fields from MSH-15 and MSH-16.
	AcceptACKType string // MSH-15
	AppACKType    string // MSH-16
	// CharacterSet from MSH-18.
	CharacterSet string // MSH-18
}

// ErrNotHL7 is returned when the input does not begin with "MSH".
var ErrNotHL7 = errors.New("er7: message does not start with MSH segment")

// ErrMSHTooShort is returned when MSH is too short to contain encoding characters.
var ErrMSHTooShort = errors.New("er7: MSH segment too short to contain encoding characters")

// Parse parses a raw HL7 v2 ER7 byte slice.
// Segments are CR-delimited (0x0D). The input must not be MLLP-framed.
//
// The function never modifies the input slice.
func Parse(raw []byte) (*Message, error) {
	if len(raw) < 8 {
		return nil, ErrMSHTooShort
	}
	if string(raw[:3]) != "MSH" {
		return nil, ErrNotHL7
	}

	// MSH-1: field separator is the byte immediately after "MSH".
	fieldSep := raw[3]

	// MSH-2: encoding characters are the four bytes after the first field separator.
	// They occupy the field that starts at raw[4].
	if len(raw) < 8 {
		return nil, ErrMSHTooShort
	}
	enc := raw[4:8]

	sep := Separators{
		Field:        fieldSep,
		Component:    enc[0],
		Repetition:   enc[1],
		Escape:       enc[2],
		Subcomponent: enc[3],
	}

	// Split the whole message into segment lines on CR (0x0D).
	// Trailing CR is tolerated.
	text := string(raw)
	lines := splitSegments(text)

	msg := &Message{Separators: sep}

	for i, line := range lines {
		if line == "" {
			continue
		}
		seg, err := parseSegment(line, sep, i == 0)
		if err != nil {
			return nil, fmt.Errorf("er7: segment %d: %w", i, err)
		}
		msg.Segments = append(msg.Segments, seg)
	}

	// Extract MSH metadata.
	if err := extractMSH(msg); err != nil {
		return nil, err
	}

	return msg, nil
}

// splitSegments splits on CR (0x0D), the HL7 segment terminator.
func splitSegments(s string) []string {
	return strings.Split(s, "\r")
}

// parseSegment parses one segment line. isMSH must be true for the first segment.
func parseSegment(line string, sep Separators, isMSH bool) (Segment, error) {
	if len(line) < 3 {
		return Segment{}, errors.New("segment name too short")
	}
	name := line[:3]
	if len(line) == 3 {
		return Segment{Name: name}, nil
	}
	if line[3] != sep.Field {
		if !isMSH {
			// Allow segments without fields (just a name).
			return Segment{Name: name}, nil
		}
		return Segment{}, errors.New("MSH-1 separator mismatch")
	}

	// For MSH, Fields[0] is the field-separator character itself (a single char),
	// and Fields[1] is the four encoding characters. These are special.
	if isMSH {
		rest := line[4:] // everything after "MSH|"
		// Find the second field separator to isolate the encoding characters.
		idx := strings.IndexByte(rest, sep.Field)
		var encField string
		var remaining string
		if idx < 0 {
			encField = rest
			remaining = ""
		} else {
			encField = rest[:idx]
			remaining = rest[idx:] // includes the separator
		}
		fields := []string{string(sep.Field), encField}
		// Parse remaining fields normally, starting from field index 3.
		if remaining != "" {
			more := splitFields(remaining[1:], sep.Field) // skip leading |
			fields = append(fields, more...)
		}
		return Segment{Name: name, Fields: fields}, nil
	}

	rest := line[4:] // skip "XXX|"
	fields := splitFields(rest, sep.Field)
	return Segment{Name: name, Fields: fields}, nil
}

// splitFields splits a segment body on the field separator, preserving empties.
func splitFields(s string, sep byte) []string {
	if s == "" {
		return nil
	}
	var fields []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			fields = append(fields, s[start:i])
			start = i + 1
		}
	}
	fields = append(fields, s[start:])
	return fields
}

// extractMSH fills in the convenience metadata fields from the parsed MSH segment.
func extractMSH(msg *Message) error {
	if len(msg.Segments) == 0 || msg.Segments[0].Name != "MSH" {
		return ErrNotHL7
	}
	msh := msg.Segments[0]
	f := msh.Fields
	// MSH fields are 1-indexed; Fields[0] is MSH-1 (field sep), Fields[1] is MSH-2.
	// Fields[2] = MSH-3, Fields[3] = MSH-4, etc.
	get := func(index int) string {
		// index is the 1-based field number; Fields[index-1] is the slice index.
		// But Fields[0] = MSH-1, Fields[1] = MSH-2, so Fields[n-1] = MSH-n.
		i := index - 1
		if i < 0 || i >= len(f) {
			return ""
		}
		return f[i]
	}
	msg.SendingApp   = get(3)
	msg.SendingFac   = get(4)
	msg.ReceivingApp = get(5)
	msg.ReceivingFac = get(6)
	msg.DateTime     = get(7)
	msg.MessageType  = get(9)
	msg.ControlID    = get(10)
	msg.ProcessingID = get(11)
	msg.Version      = get(12)
	msg.AcceptACKType = get(15)
	msg.AppACKType   = get(16)
	msg.CharacterSet = get(18)
	return nil
}

// FieldAt returns the raw field string at 1-based field number n of the named
// segment (first occurrence). Returns "" if not present.
func (msg *Message) FieldAt(segmentName string, fieldNum int) string {
	for _, seg := range msg.Segments {
		if seg.Name == segmentName {
			i := fieldNum - 1
			if i < 0 || i >= len(seg.Fields) {
				return ""
			}
			return seg.Fields[i]
		}
	}
	return ""
}

// SegmentsNamed returns all segments with the given name in document order.
func (msg *Message) SegmentsNamed(name string) []Segment {
	var out []Segment
	for _, seg := range msg.Segments {
		if seg.Name == name {
			out = append(out, seg)
		}
	}
	return out
}
