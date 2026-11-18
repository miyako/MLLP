package framing

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// helpers

func encode(payload []byte) []byte {
	var b bytes.Buffer
	if err := Encode(&b, payload); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func decodeAll(r io.Reader, maxFrame int) ([][]byte, error) {
	d := NewDecoder(maxFrame)
	var frames [][]byte
	err := d.Decode(r, func(payload []byte) error {
		cp := make([]byte, len(payload))
		copy(cp, payload)
		frames = append(frames, cp)
		return nil
	})
	if err == io.EOF {
		err = nil
	}
	return frames, err
}

// --- round-trip ---

func TestRoundTrip(t *testing.T) {
	msg := []byte("MSH|^~\\&|HIS|CENTRAL|LAB|PATH|20260101120000||ADT^A01|MSG001|P|2.5.1\rPID|||12345||Doe^John\r")
	frames, err := decodeAll(bytes.NewReader(encode(msg)), DefaultMaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || !bytes.Equal(frames[0], msg) {
		t.Fatalf("round-trip mismatch: got %q", frames[0])
	}
}

// --- multiple frames in one read ---

func TestMultipleFramesInOneRead(t *testing.T) {
	a := []byte("MSG-A")
	b := []byte("MSG-B")
	c := []byte("MSG-C")
	var buf bytes.Buffer
	for _, m := range [][]byte{a, b, c} {
		if err := Encode(&buf, m); err != nil {
			t.Fatal(err)
		}
	}
	frames, err := decodeAll(&buf, DefaultMaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(frames))
	}
	for i, want := range [][]byte{a, b, c} {
		if !bytes.Equal(frames[i], want) {
			t.Errorf("frame %d: got %q want %q", i, frames[i], want)
		}
	}
}

// --- byte-at-a-time reader (models heavily fragmented TCP) ---

type byteReader struct{ data []byte }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestByteAtATime(t *testing.T) {
	msg := []byte("MSH|^~\\&|SENDER|FAC|RECV|FAC|20260101||ORU^R01|ID1|P|2.5.1\r")
	raw := encode(msg)
	frames, err := decodeAll(&byteReader{raw}, DefaultMaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || !bytes.Equal(frames[0], msg) {
		t.Fatalf("byte-at-a-time: got %q", frames[0])
	}
}

// --- split immediately before FS ---

func TestSplitBeforeFS(t *testing.T) {
	msg := []byte("short payload")
	full := encode(msg)
	// Split just before FS: everything up to but not including FS.
	splitAt := len(full) - 2 // FS is at len-2, CR at len-1
	part1 := full[:splitAt]
	part2 := full[splitAt:]

	var received [][]byte
	d := NewDecoder(DefaultMaxFrame)
	for _, chunk := range [][]byte{part1, part2} {
		if err := d.feed(chunk, func(payload []byte) error {
			cp := make([]byte, len(payload))
			copy(cp, payload)
			received = append(received, cp)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(received) != 1 || !bytes.Equal(received[0], msg) {
		t.Fatalf("split-before-FS: got %q", received)
	}
}

// --- junk bytes before VT are discarded ---

func TestJunkBeforeVT(t *testing.T) {
	msg := []byte("hello")
	raw := append([]byte("JUNK\r\n"), encode(msg)...)
	frames, err := decodeAll(bytes.NewReader(raw), DefaultMaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || !bytes.Equal(frames[0], msg) {
		t.Fatalf("junk-before-VT: got %q", frames[0])
	}
}

// --- unexpected VT inside a frame resets accumulation ---

func TestUnexpectedVTInsideFrame(t *testing.T) {
	msg := []byte("valid")
	// A VT mid-stream: inject VT + "garbage", then the real frame.
	var raw []byte
	raw = append(raw, VT)                 // start frame
	raw = append(raw, []byte("garbage")...) // partial payload
	raw = append(raw, VT)                 // unexpected VT resets
	raw = append(raw, msg...)             // new payload
	raw = append(raw, FS, CR)
	frames, err := decodeAll(bytes.NewReader(raw), DefaultMaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || !bytes.Equal(frames[0], msg) {
		t.Fatalf("unexpected-VT: got %q", frames)
	}
}

// --- missing CR after FS is a fatal error ---

func TestMissingCR(t *testing.T) {
	raw := []byte{VT, 'A', 'B', FS, 'X'} // X instead of CR
	_, err := decodeAll(bytes.NewReader(raw), DefaultMaxFrame)
	if !errors.Is(err, ErrMissingCR) {
		t.Fatalf("expected ErrMissingCR, got %v", err)
	}
}

// --- connection closing mid-frame ---

func TestConnectionCloseInsideFrame(t *testing.T) {
	raw := []byte{VT, 'A', 'B', 'C'} // no FS+CR
	_, err := decodeAll(bytes.NewReader(raw), DefaultMaxFrame)
	if !errors.Is(err, ErrIncompleteFrame) {
		t.Fatalf("expected ErrIncompleteFrame, got %v", err)
	}
}

// --- clean EOF while idle ---

func TestCleanEOFWhileIdle(t *testing.T) {
	_, err := decodeAll(strings.NewReader(""), DefaultMaxFrame)
	if err != nil {
		t.Fatalf("clean EOF while idle: got %v", err)
	}
}

// --- oversized frame ---

func TestOversizedFrame(t *testing.T) {
	// Build a payload that exceeds the limit.
	payload := bytes.Repeat([]byte("X"), 10)
	raw := make([]byte, 0, 13)
	raw = append(raw, VT)
	raw = append(raw, payload...)
	raw = append(raw, FS, CR)
	_, err := decodeAll(bytes.NewReader(raw), 5) // limit: 5 bytes
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

// --- encode rejects FS and VT in payload ---

func TestEncodeRejectsFS(t *testing.T) {
	payload := []byte{'A', FS, 'B'}
	var b bytes.Buffer
	if err := Encode(&b, payload); err == nil {
		t.Fatal("Encode accepted payload containing FS")
	}
}

func TestEncodeRejectsVT(t *testing.T) {
	payload := []byte{'A', VT, 'B'}
	var b bytes.Buffer
	if err := Encode(&b, payload); err == nil {
		t.Fatal("Encode accepted payload containing VT")
	}
}

// --- onFrame error propagates ---

func TestOnFrameErrorPropagates(t *testing.T) {
	msg := []byte("MSG")
	r := bytes.NewReader(encode(msg))
	d := NewDecoder(DefaultMaxFrame)
	sentinel := errors.New("application rejected frame")
	err := d.Decode(r, func(_ []byte) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}

// --- empty payload is valid ---

func TestEmptyPayload(t *testing.T) {
	frames, err := decodeAll(bytes.NewReader(encode([]byte{})), DefaultMaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || len(frames[0]) != 0 {
		t.Fatalf("empty payload: got %v", frames)
	}
}

// --- payload containing CR (segment terminator) survives intact ---

func TestPayloadContainingCR(t *testing.T) {
	msg := []byte("MSH|...\rPID|...\rOBX|...\r")
	frames, err := decodeAll(bytes.NewReader(encode(msg)), DefaultMaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || !bytes.Equal(frames[0], msg) {
		t.Fatalf("CR-in-payload: got %q", frames[0])
	}
}

// --- fuzz-style: random byte prefix + valid frame ---

func TestRandomPrefixBeforeVT(t *testing.T) {
	prefix := []byte{0x00, 0x01, 0xFF, 0xFE, CR, FS, 0x7F}
	msg := []byte("PAYLOAD")
	raw := append(prefix, encode(msg)...)
	frames, err := decodeAll(bytes.NewReader(raw), DefaultMaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || !bytes.Equal(frames[0], msg) {
		t.Fatalf("random-prefix: got %q", frames)
	}
}
