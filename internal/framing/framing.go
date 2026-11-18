// Package framing implements MLLP (Minimal Lower Layer Protocol) message framing.
//
// Wire format: VT (0x0B) + HL7-payload + FS (0x1C) + CR (0x0D)
// The payload may itself contain CR segment terminators.
//
// The framer is a streaming state machine. It does not assume that one TCP
// read equals one message frame. It handles frames split across many reads,
// multiple frames in one read, and every specified edge case.
//
// This package must have no knowledge of HL7 or JSON. It operates only on bytes.
package framing

import (
	"errors"
	"fmt"
	"io"
)

// Wire-protocol byte constants.
const (
	VT = 0x0B // Start Block character
	FS = 0x1C // End Block character
	CR = 0x0D // Carriage Return terminator
)

// DefaultMaxFrame is the default maximum permitted frame payload size (1 MiB).
// Clinical messages may contain OBX segments with embedded documents.
const DefaultMaxFrame = 1 << 20 // 1 MiB

// ErrFrameTooLarge is returned when an incoming frame exceeds MaxFrame bytes.
var ErrFrameTooLarge = errors.New("mllp: frame payload exceeds maximum size")

// ErrIncompleteFrame is returned when the connection closes mid-frame.
var ErrIncompleteFrame = errors.New("mllp: connection closed inside an MLLP frame")

// ErrMissingCR is returned when FS is not followed by CR.
var ErrMissingCR = errors.New("mllp: FS not followed by CR")

// state tracks the streaming decoder position.
type state int

const (
	stateIdle    state = iota // waiting for VT
	statePayload              // inside a frame, accumulating payload bytes
	stateFS                   // saw FS, expecting CR
)

// Decoder is a streaming MLLP frame decoder. It is not safe for concurrent use.
// Create one Decoder per connection.
type Decoder struct {
	maxFrame int
	state    state
	buf      []byte
}

// NewDecoder returns a Decoder with the given maximum frame payload size.
// Pass DefaultMaxFrame unless you have a specific requirement.
func NewDecoder(maxFrame int) *Decoder {
	if maxFrame <= 0 {
		maxFrame = DefaultMaxFrame
	}
	return &Decoder{maxFrame: maxFrame}
}

// Decode reads bytes from r and calls onFrame for each complete MLLP frame
// payload. onFrame receives a slice that is only valid for the duration of the
// call; the caller must copy it if the data needs to outlive the callback.
//
// Decode returns nil only on clean EOF after a complete frame or while idle.
// It returns io.EOF if r closes while idle (no partial frame), which the caller
// should treat as a normal connection close.
// All other errors are fatal for this connection.
func (d *Decoder) Decode(r io.Reader, onFrame func(payload []byte) error) error {
	raw := make([]byte, 4096)
	for {
		n, err := r.Read(raw)
		if n > 0 {
			if ferr := d.feed(raw[:n], onFrame); ferr != nil {
				return ferr
			}
		}
		if err != nil {
			if err == io.EOF {
				if d.state != stateIdle {
					return ErrIncompleteFrame
				}
				return io.EOF
			}
			return fmt.Errorf("mllp: read error: %w", err)
		}
	}
}

// feed processes a chunk of bytes from the network, advancing the state machine.
func (d *Decoder) feed(chunk []byte, onFrame func(payload []byte) error) error {
	for _, b := range chunk {
		switch d.state {
		case stateIdle:
			if b == VT {
				// Start of a new frame. Reset buffer but keep capacity.
				d.buf = d.buf[:0]
				d.state = statePayload
			}
			// Bytes before VT are silently discarded (pre-frame junk).

		case statePayload:
			switch b {
			case VT:
				// Unexpected VT inside a frame. Discard the incomplete frame
				// and treat this as the start of a new one.
				d.buf = d.buf[:0]
				// stay in statePayload
			case FS:
				d.state = stateFS
			default:
				if len(d.buf) >= d.maxFrame {
					return ErrFrameTooLarge
				}
				d.buf = append(d.buf, b)
			}

		case stateFS:
			if b != CR {
				return ErrMissingCR
			}
			// Complete frame.
			if err := onFrame(d.buf); err != nil {
				return err
			}
			d.buf = d.buf[:0]
			d.state = stateIdle
		}
	}
	return nil
}

// Encode writes a single MLLP-framed message to w.
// payload must not contain FS (0x1C) bytes; this is enforced.
func Encode(w io.Writer, payload []byte) error {
	for _, b := range payload {
		if b == FS {
			return errors.New("mllp: payload contains reserved FS byte (0x1C)")
		}
	}
	frame := make([]byte, 0, 1+len(payload)+2)
	frame = append(frame, VT)
	frame = append(frame, payload...)
	frame = append(frame, FS, CR)
	_, err := w.Write(frame)
	return err
}
