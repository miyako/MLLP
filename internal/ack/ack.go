// Package ack implements HL7 v2 ACK generation and correlation.
//
// Critical rules:
//   - An AA response means the receiving application accepted the message.
//     It must NOT be emitted just because TCP received bytes or the frame parsed.
//   - The host (4D) decides the ACK code after durable persistence.
//   - ACK correlation uses MSA-2 (the original MSH-10 control ID), not the
//     CLI's internal message ID.
//   - Original-mode ACKs (AA/AE/AR) are implemented first.
//   - PHI must never appear in ACK error text; use stable error codes only.
package ack

import (
	"errors"
	"fmt"
	"time"

	"github.com/miyako/mllp/internal/hl7/er7"
)

// Code represents an HL7 original-mode acknowledgement code.
type Code string

const (
	// AA — Application Accept: message accepted and committed.
	AA Code = "AA"
	// AE — Application Error: message received but rejected due to an error.
	AE Code = "AE"
	// AR — Application Reject: message rejected (e.g. unknown message type).
	AR Code = "AR"
)

// ErrInvalidCode is returned when an unknown ACK code is supplied.
var ErrInvalidCode = errors.New("ack: invalid acknowledgement code")

// ErrNoControlID is returned when the original message has no MSH-10.
var ErrNoControlID = errors.New("ack: original message has no control ID (MSH-10)")

// Options controls ACK message generation.
type Options struct {
	// Code is the acknowledgement code (AA, AE, AR). Required.
	Code Code
	// SendingApp overrides the sending application for the ACK (MSH-3).
	// If empty, the original message's receiving application is used.
	SendingApp string
	// SendingFac overrides the sending facility for the ACK (MSH-4).
	// If empty, the original message's receiving facility is used.
	SendingFac string
	// ACKControlID is the control ID for the ACK message itself (MSH-10).
	// If empty, a unique ID is generated.
	ACKControlID string
	// ErrorCode is the HL7 error code (ERR segment) for AE/AR responses.
	// Must not contain PHI.
	ErrorCode string
	// ErrorText is a brief human-readable description for AE/AR responses.
	// Must not contain PHI.
	ErrorText string
	// Timestamp overrides the ACK generation time. Zero means time.Now().
	Timestamp time.Time
}

// Build constructs a minimal HL7 ACK message for the given original message.
// The result is a raw ER7 string ready for MLLP framing.
//
// The function:
//   - Swaps sending/receiving app and facility fields.
//   - Echoes the original MSH-10 in MSA-2.
//   - Preserves the original message version and separators.
//   - Uses a new unique MSH-10 for the ACK itself.
//   - Adds an ERR segment for AE/AR when ErrorCode is set.
func Build(original *er7.Message, opts Options) (string, error) {
	switch opts.Code {
	case AA, AE, AR:
	default:
		return "", ErrInvalidCode
	}

	controlID := original.ControlID
	if controlID == "" {
		return "", ErrNoControlID
	}

	ts := opts.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	ackTS := ts.Format("20060102150405")

	// Determine sending/receiving fields for the ACK.
	// ACK sender = original receiver; ACK receiver = original sender.
	sendApp := opts.SendingApp
	if sendApp == "" {
		sendApp = original.ReceivingApp
	}
	sendFac := opts.SendingFac
	if sendFac == "" {
		sendFac = original.ReceivingFac
	}
	recvApp := original.SendingApp
	recvFac := original.SendingFac

	ackID := opts.ACKControlID
	if ackID == "" {
		ackID = fmt.Sprintf("ACK%s", ts.Format("20060102150405.000"))
	}

	version := original.Version
	if version == "" {
		version = "2.5.1"
	}

	sep := original.Separators
	encChars := string([]byte{sep.Component, sep.Repetition, sep.Escape, sep.Subcomponent})
	f := string(sep.Field)

	// Build MSH segment for the ACK.
	// Fields: MSH-1=|, MSH-2=^~\&, MSH-3..MSH-9, MSH-10=ackID, MSH-11=P, MSH-12=version
	msh := "MSH" + f +
		encChars + f + // MSH-2
		sendApp + f + // MSH-3
		sendFac + f + // MSH-4
		recvApp + f + // MSH-5
		recvFac + f + // MSH-6
		ackTS + f + // MSH-7
		f + // MSH-8 (security — empty)
		"ACK" + f + // MSH-9
		ackID + f + // MSH-10
		original.ProcessingID + f + // MSH-11
		version // MSH-12

	// Build MSA segment.
	// MSA-1 = ack code, MSA-2 = original control ID.
	msa := "MSA" + f + string(opts.Code) + f + controlID

	msg := msh + "\r" + msa + "\r"

	// Add ERR segment for AE/AR responses when error codes are provided.
	if opts.Code != AA && opts.ErrorCode != "" {
		err := "ERR" + f + f + f + opts.ErrorCode
		if opts.ErrorText != "" {
			err += f + opts.ErrorText
		}
		msg += err + "\r"
	}

	return msg, nil
}

// Validate checks whether a received ACK correlates with a sent message.
// originalControlID is the MSH-10 of the message that was sent.
// ackMessage is the parsed ACK received from the remote system.
func Validate(originalControlID string, ackMessage *er7.Message) error {
	if ackMessage.MessageType != "ACK" {
		return fmt.Errorf("ack: expected message type ACK, got %q", ackMessage.MessageType)
	}
	msa2 := ackMessage.FieldAt("MSA", 2)
	if msa2 == "" {
		return errors.New("ack: MSA-2 missing in received ACK")
	}
	if msa2 != originalControlID {
		return fmt.Errorf("ack: correlation mismatch: sent %q received ACK for %q", originalControlID, msa2)
	}
	return nil
}

// CodeFromMSA1 extracts the ACK code from MSA-1 of a received ACK message.
func CodeFromMSA1(ackMessage *er7.Message) (Code, error) {
	code := ackMessage.FieldAt("MSA", 1)
	switch Code(code) {
	case AA, AE, AR:
		return Code(code), nil
	case "CA", "CE", "CR":
		// Enhanced mode codes — not yet implemented.
		return "", fmt.Errorf("ack: enhanced-mode code %q is not yet supported", code)
	default:
		return "", fmt.Errorf("ack: unrecognised MSA-1 value %q", code)
	}
}
