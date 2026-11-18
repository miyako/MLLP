// Package charset handles HL7 v2 character set identification and normalization.
//
// HL7 v2 MSH-18 uses its own encoding identifier names which do not always
// match standard IANA charset names. This package provides the mapping.
//
// The transport framer always operates on raw bytes; charset interpretation
// only happens when converting ER7 to canonical JSON or when building ACKs.
//
// Supported sets for v1:
//   - ASCII / empty (default for HL7 v2.x < 2.5)
//   - 8859/1  (ISO-8859-1 / Latin-1)
//   - 8859/15 (ISO-8859-15)
//   - UNICODE UTF-8
//   - UTF-8   (alias)
//   - SHIFT_JIS, JIS, ISO IR87
//
// Non-UTF-8 payloads are base64-encoded in canonical JSON with the original
// charset metadata preserved.
package charset

import (
	"errors"
	"strings"
)

// ErrUnsupported is returned for charset identifiers the package does not handle.
var ErrUnsupported = errors.New("charset: unsupported encoding")

// Identity represents a resolved charset.
type Identity struct {
	// HL7Name is the MSH-18 value as written in the message.
	HL7Name string
	// IANAName is the closest IANA equivalent (informational).
	IANAName string
	// IsUTF8 is true when the charset is compatible with UTF-8 decoding.
	IsUTF8 bool
}

// knownCharsets maps lower-cased MSH-18 values to Identity records.
var knownCharsets = map[string]Identity{
	"":              {"", "US-ASCII", true},
	"ascii":         {"ASCII", "US-ASCII", true},
	"unicode utf-8": {"UNICODE UTF-8", "UTF-8", true},
	"utf-8":         {"UTF-8", "UTF-8", true},
	"8859/1":        {"8859/1", "ISO-8859-1", false},
	"iso-8859-1":    {"ISO-8859-1", "ISO-8859-1", false},
	"8859/15":       {"8859/15", "ISO-8859-15", false},
	"iso-8859-15":   {"ISO-8859-15", "ISO-8859-15", false},
	"shift_jis":     {"SHIFT_JIS", "Shift_JIS", false},
	"jis":           {"JIS", "ISO-2022-JP", false},
	"iso ir87":      {"ISO IR87", "ISO-2022-JP", false},
	"windows-1252":  {"Windows-1252", "windows-1252", false},
}

// Resolve looks up the charset identity for the given MSH-18 value.
// An empty string is valid (implies ASCII for pre-2.5 HL7).
func Resolve(msh18 string) (Identity, error) {
	id, ok := knownCharsets[strings.ToLower(strings.TrimSpace(msh18))]
	if !ok {
		return Identity{}, ErrUnsupported
	}
	return id, nil
}

// IsUTF8Compatible reports whether msh18 identifies a UTF-8-compatible charset.
// Returns false for unknown or non-UTF-8 charsets.
func IsUTF8Compatible(msh18 string) bool {
	id, err := Resolve(msh18)
	if err != nil {
		return false
	}
	return id.IsUTF8
}
