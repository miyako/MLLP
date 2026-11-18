package framing

import (
	"bytes"
	"io"
	"testing"
)

// FuzzDecoder feeds arbitrary byte sequences to the streaming MLLP decoder.
// The decoder must never panic or hang; it must either produce valid frames
// or return a defined error.
func FuzzDecoder(f *testing.F) {
	// Seed with known interesting inputs.
	seeds := [][]byte{
		{},
		{VT, FS, CR},
		{VT, 'A', 'B', FS, CR},
		{VT, VT, 'X', FS, CR},
		{VT, 'A', FS, 'X'},          // missing CR
		{VT, 'A'},                    // incomplete frame
		{0x00, VT, 'P', 'A', FS, CR}, // junk before VT
		encode([]byte("MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\r")),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		d := NewDecoder(1024) // small limit to exercise overflow
		_ = d.Decode(bytes.NewReader(data), func(payload []byte) error {
			// Payload must not be nil and must not contain FS.
			for _, b := range payload {
				if b == FS {
					t.Errorf("decoder produced payload containing FS byte")
				}
			}
			return nil
		})
	})
}

// FuzzEncodeDecode verifies that any payload that Encode accepts can be
// decoded back to the identical bytes.
func FuzzEncodeDecode(f *testing.F) {
	f.Add([]byte("hello"))
	f.Add([]byte("MSH|^~\\&|A|B\r"))
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte("X"), 512))

	f.Fuzz(func(t *testing.T, payload []byte) {
		// Skip payloads containing FS or VT — Encode correctly rejects them.
		for _, b := range payload {
			if b == FS || b == VT {
				return
			}
		}
		var buf bytes.Buffer
		if err := Encode(&buf, payload); err != nil {
			t.Fatal(err)
		}
		d := NewDecoder(len(payload) + 10)
		var got []byte
		err := d.Decode(bytes.NewReader(buf.Bytes()), func(p []byte) error {
			cp := make([]byte, len(p))
			copy(cp, p)
			got = cp
			return io.EOF
		})
		if err != nil && err != io.EOF {
			t.Fatalf("decode error: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("round-trip mismatch:\ngot:  %v\nwant: %v", got, payload)
		}
	})
}
