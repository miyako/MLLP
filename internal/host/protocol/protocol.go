// Package protocol defines and handles the versioned NDJSON protocol between
// the mllp CLI and its host application (e.g. a 4D SystemWorker).
//
// Stream responsibilities:
//   stdin  — host commands and ACK decisions
//   stdout — CLI events and inbound HL7 messages
//   stderr — structured diagnostic logs only (no PHI, no raw payloads)
//
// Every message is a single UTF-8 JSON object terminated by LF (\n).
// stdout must never contain human-readable logs, banners, or progress text.
package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

const (
	// ProtocolVersion is the current protocol version number.
	ProtocolVersion = 1

	// MaxStdioFrame is the maximum permitted NDJSON frame size (4 MiB).
	MaxStdioFrame = 4 << 20

	// MaxPendingMessages is the default capacity of the outbound event channel.
	MaxPendingMessages = 100
)

// identifier validates protocol IDs (epoch, message IDs, connection IDs).
var identifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,96}$`)

// --- Outgoing (CLI → host) message kinds ---

const (
	KindReady   = "ready"   // listener or client is operational
	KindMessage = "message" // inbound HL7 message
	KindSent    = "sent"    // outbound message transmitted
	KindAck     = "ack"     // remote ACK received
	KindError   = "error"   // structured error
	KindStatus  = "status"  // connection lifecycle event
	KindStopped = "stopped" // graceful shutdown complete
)

// --- Incoming (host → CLI) message kinds ---

const (
	KindHello    = "hello"    // initial host handshake
	KindSend     = "send"     // send an outbound message
	KindAckHost  = "ack"      // acknowledge an inbound message (host-side)
	KindPing     = "ping"     // liveness check
	KindShutdown = "shutdown" // graceful shutdown request
)

// Envelope is the common header present in every protocol message.
type Envelope struct {
	Protocol int    `json:"protocol"`
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`
	// Seq and Epoch are required in host-to-CLI messages.
	Seq   uint64 `json:"seq,omitempty"`
	Epoch string `json:"epoch,omitempty"`
}

// ReadyMsg is emitted by the CLI when it has successfully started.
type ReadyMsg struct {
	Envelope
	Version string `json:"version"`
	// Mode is "listen" or "connect".
	Mode    string `json:"mode"`
	Address string `json:"address,omitempty"` // bound address for listen mode
}

// StatusMsg is emitted on connection lifecycle changes.
type StatusMsg struct {
	Envelope
	ConnectionID string `json:"connectionId,omitempty"`
	Event        string `json:"event"` // "connected", "disconnected", "error"
	Peer         string `json:"peer,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// ErrorMsg is emitted for structured, machine-readable errors.
type ErrorMsg struct {
	Envelope
	Code    string `json:"code"`
	Message string `json:"message"`
	// ConnectionID is set when the error is connection-specific.
	ConnectionID string `json:"connectionId,omitempty"`
}

// StoppedMsg is emitted after graceful shutdown completes.
type StoppedMsg struct {
	Envelope
}

// HelloMsg is the first message sent by the host to the CLI.
type HelloMsg struct {
	Envelope
	// No additional fields required in v1.
}

// AckHostMsg is sent by the host to acknowledge an inbound HL7 message.
type AckHostMsg struct {
	Envelope
	// ID references the CLI message ID (not MSH-10).
	AckCode string `json:"ackCode"` // "AA", "AE", "AR"
	// ErrorCode and ErrorText are set for AE/AR responses.
	ErrorCode string `json:"errorCode,omitempty"`
	ErrorText string `json:"errorText,omitempty"`
}

// ShutdownMsg is sent by the host to request graceful shutdown.
type ShutdownMsg struct {
	Envelope
}

// --- Writer ---

// Writer serialises outgoing CLI→host messages to stdout as NDJSON.
// It is safe for concurrent use: a single goroutine must own it and drain
// the Events channel.
type Writer struct {
	enc *json.Encoder
}

// NewWriter returns a Writer that writes to w.
// HTML escaping is disabled; HL7 characters like < > & must survive intact.
func NewWriter(w io.Writer) *Writer {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Writer{enc: enc}
}

// Write serialises v as a single NDJSON line. It is not safe for concurrent use;
// the caller must serialise access.
func (wr *Writer) Write(v any) error {
	return wr.enc.Encode(v)
}

// --- Reader ---

// HostCommand is the parsed form of one host-to-CLI message.
// All fields are raw JSON for the kind-specific payload.
type HostCommand struct {
	Envelope
	Raw json.RawMessage
}

// Reader reads and validates host-to-CLI NDJSON frames from stdin.
type Reader struct {
	scanner *bufio.Scanner
	epoch   string
	seq     uint64
}

// NewReader returns a Reader that reads from r.
func NewReader(r io.Reader) *Reader {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 4096)
	scanner.Buffer(buf, MaxStdioFrame+1)
	scanner.Split(func(data []byte, eof bool) (advance int, token []byte, err error) {
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
			return idx + 1, data[:idx], nil
		}
		if eof && len(data) > 0 {
			return 0, nil, errors.New("incomplete NDJSON frame at EOF")
		}
		return 0, nil, nil
	})
	return &Reader{scanner: scanner}
}

// Read reads and validates the next host command from stdin.
// Returns io.EOF on clean stdin closure (normal shutdown signal).
func (rd *Reader) Read() (*HostCommand, error) {
	if !rd.scanner.Scan() {
		if err := rd.scanner.Err(); err != nil {
			return nil, fmt.Errorf("stdin framing: %w", err)
		}
		return nil, io.EOF // clean shutdown signal
	}

	line := rd.scanner.Bytes()
	if len(line) > MaxStdioFrame {
		return nil, errors.New("protocol: stdin frame exceeds maximum size")
	}
	if !utf8.Valid(line) {
		return nil, errors.New("protocol: stdin frame is not valid UTF-8")
	}

	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, fmt.Errorf("protocol: malformed stdin frame: %w", err)
	}

	if err := rd.validate(env); err != nil {
		return nil, err
	}

	return &HostCommand{Envelope: env, Raw: json.RawMessage(line)}, nil
}

// validate checks the protocol envelope invariants.
func (rd *Reader) validate(env Envelope) error {
	if env.Protocol != ProtocolVersion {
		return fmt.Errorf("protocol: unsupported protocol version %d", env.Protocol)
	}
	if env.Kind == "" {
		return errors.New("protocol: missing kind")
	}

	if env.Kind == KindHello {
		if rd.epoch != "" {
			return errors.New("protocol: duplicate hello")
		}
		if env.Seq != 1 {
			return errors.New("protocol: hello must have seq 1")
		}
		if !identifier.MatchString(env.Epoch) {
			return errors.New("protocol: hello missing or invalid epoch")
		}
		rd.epoch = env.Epoch
		rd.seq = 1
		return nil
	}

	// All non-hello messages require epoch+seq.
	if rd.epoch == "" {
		return errors.New("protocol: hello must precede all other messages")
	}
	if env.Epoch != rd.epoch {
		return errors.New("protocol: epoch mismatch; restart the CLI and send a fresh hello")
	}
	if env.Seq != rd.seq+1 {
		return fmt.Errorf("protocol: sequence mismatch: expected %d got %d", rd.seq+1, env.Seq)
	}
	rd.seq = env.Seq
	return nil
}

// ValidateID checks that a string is a valid protocol identifier.
func ValidateID(s string) bool {
	return identifier.MatchString(s)
}
