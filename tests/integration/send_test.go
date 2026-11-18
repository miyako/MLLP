package integration

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/miyako/mllp/internal/ack"
	"github.com/miyako/mllp/internal/framing"
	"github.com/miyako/mllp/internal/hl7/er7"
)

// TestSendOneShot verifies the one-shot send command:
//  1. Set up a mock MLLP server that receives one message and returns AA.
//  2. Run `mllp send --host ... --file ...`.
//  3. Verify the CLI exits 0 and stdout contains an "ack" event with ackCode AA.
func TestSendOneShot(t *testing.T) {
	bin := buildCLI(t)

	// Mock MLLP server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverReceived := make(chan []byte, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()

		// Receive one MLLP frame.
		dec := framing.NewDecoder(framing.DefaultMaxFrame)
		var received []byte
		_ = dec.Decode(nc, func(payload []byte) error {
			cp := make([]byte, len(payload))
			copy(cp, payload)
			received = cp
			return io.EOF // stop after first frame
		})
		serverReceived <- received

		// Build and send AA ACK.
		msg, err := er7.Parse(received)
		if err != nil || msg.ControlID == "" {
			return
		}
		ackPayload, err := ack.Build(msg, ack.Options{
			Code:         ack.AA,
			ACKControlID: "SVRACK001",
		})
		if err != nil {
			return
		}
		nc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = framing.Encode(nc, []byte(ackPayload))
		time.Sleep(300 * time.Millisecond) // let client read before server closes
	}()

	// Write a temp HL7 file.
	raw := "MSH|^~\\&|SENDER|FAC|RECEIVER|FAC|20261117||ADT^A01|SENDID001|P|2.5.1\rPID|1||99999||Smith^Jane\r"
	f, err := os.CreateTemp("", "mllp-send-*.hl7")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString(raw)
	f.Close()

	host, port, _ := net.SplitHostPort(ln.Addr().String())

	// Run `mllp send`.
	ctx_done := make(chan error, 1)
	var outBuf strings.Builder
	cmd := exec.Command(bin, "send", "--host", host, "--port", port,
		"--file", f.Name(), "--wait-ack")
	cmd.Stdout = &outBuf
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { ctx_done <- cmd.Wait() }()

	select {
	case err := <-ctx_done:
		if err != nil {
			t.Fatalf("mllp send failed: %v\noutput: %s", err, outBuf.String())
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("mllp send timed out")
	}

	// Verify stdout contains an "ack" event.
	output := strings.TrimSpace(outBuf.String())
	var m map[string]any
	if err := json.Unmarshal([]byte(output), &m); err != nil {
		t.Fatalf("invalid JSON output: %v\n%q", err, output)
	}
	if m["kind"] != "ack" {
		t.Errorf("kind: got %v want ack", m["kind"])
	}
	if m["ackCode"] != "AA" {
		t.Errorf("ackCode: got %v want AA", m["ackCode"])
	}
	if m["controlId"] != "SENDID001" {
		t.Errorf("controlId: got %v want SENDID001", m["controlId"])
	}

	// Verify the server received the correct message.
	select {
	case received := <-serverReceived:
		if !strings.Contains(string(received), "SENDID001") {
			t.Errorf("server did not receive expected control ID:\n%s", received)
		}
	case <-time.After(5 * time.Second):
		t.Error("server did not receive message within timeout")
	}
}

// TestSendNoWaitACK verifies --wait-ack=false exits after sending without waiting.
func TestSendNoWaitACK(t *testing.T) {
	bin := buildCLI(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Server accepts but deliberately never sends an ACK.
	serverReceived := make(chan struct{}, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		dec := framing.NewDecoder(framing.DefaultMaxFrame)
		_ = dec.Decode(nc, func(_ []byte) error {
			serverReceived <- struct{}{}
			return io.EOF
		})
		time.Sleep(2 * time.Second) // keep connection open briefly
	}()

	raw := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|NWAIT001|P|2.5.1\rPID|1||111||Test^User\r"
	f, err := os.CreateTemp("", "mllp-nowait-*.hl7")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString(raw)
	f.Close()

	host, port, _ := net.SplitHostPort(ln.Addr().String())

	done := make(chan error, 1)
	var outBuf strings.Builder
	cmd := exec.Command(bin, "send", "--host", host, "--port", port,
		"--file", f.Name(), "--wait-ack=false")
	cmd.Stdout = &outBuf
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("mllp send (no-wait) failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("mllp send --wait-ack=false timed out (should have exited quickly)")
	}

	output := strings.TrimSpace(outBuf.String())
	var m map[string]any
	if err := json.Unmarshal([]byte(output), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%q", err, output)
	}
	if m["kind"] != "sent" {
		t.Errorf("kind: got %v want sent", m["kind"])
	}

	// Confirm server got the message.
	select {
	case <-serverReceived:
	case <-time.After(5 * time.Second):
		t.Error("server did not receive message")
	}
}
