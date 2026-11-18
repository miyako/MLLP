// Package integration tests the full vertical slice:
// external MLLP client → CLI listener → NDJSON host protocol → ACK back to client.
//
// This test does NOT require 4D. It plays the role of a host application by
// driving the CLI's stdin/stdout directly.
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/miyako/mllp/internal/framing"
)

// buildCLI compiles the mllp binary into a temp directory and returns the path.
func buildCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "mllp"
	if runtime.GOOS == "windows" {
		name = "mllp.exe"
	}
	bin := filepath.Join(dir, name)
	// Find repo root (two levels up from tests/integration/).
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/mllp")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build CLI: %v", err)
	}
	return bin
}

// ndjsonLine reads one complete JSON line from r.
func ndjsonLine(t *testing.T, r *bufio.Reader, timeout time.Duration) map[string]any {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("read NDJSON: %v", res.err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSuffix(res.line, "\n")), &m); err != nil {
			t.Fatalf("parse NDJSON %q: %v", res.line, err)
		}
		return m
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for NDJSON line after %v", timeout)
		return nil
	}
}

// writeNDJSON writes one JSON object as a NDJSON line to w.
func writeNDJSON(t *testing.T, w io.Writer, v map[string]any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if _, err := fmt.Fprintf(w, "%s\n", b); err != nil {
		t.Fatalf("write NDJSON: %v", err)
	}
}

// sendMLLP writes an MLLP-framed payload to nc.
func sendMLLP(t *testing.T, nc net.Conn, payload []byte) {
	t.Helper()
	if err := framing.Encode(nc, payload); err != nil {
		t.Fatalf("sendMLLP: %v", err)
	}
}

// recvMLLP reads one MLLP frame from nc within the given timeout.
func recvMLLP(t *testing.T, nc net.Conn, timeout time.Duration) []byte {
	t.Helper()
	nc.SetReadDeadline(time.Now().Add(timeout))
	dec := framing.NewDecoder(framing.DefaultMaxFrame)
	var got []byte
	err := dec.Decode(nc, func(payload []byte) error {
		cp := make([]byte, len(payload))
		copy(cp, payload)
		got = cp
		return io.EOF
	})
	if err != nil && err != io.EOF {
		t.Fatalf("recvMLLP: %v", err)
	}
	return got
}

// TestListenVerticalSlice is the first demonstrable deliverable from the plan:
//
//  1. Start the CLI as a child process (simulating 4D SystemWorker).
//  2. CLI opens a TCP listener and emits "ready".
//  3. Host sends "hello".
//  4. External MLLP client sends a synthetic ADT^A01.
//  5. CLI emits a "message" event to the host (stdout).
//  6. Host sends an "ack" with ackCode "AA".
//  7. CLI constructs and transmits the HL7 ACK to the external client.
//  8. External client receives the ACK and verifies MSA-2.
//  9. Host sends "shutdown".
//  10. CLI emits "stopped" and exits cleanly.
func TestListenVerticalSlice(t *testing.T) {
	bin := buildCLI(t)

	// Use port 0 so the OS assigns a free port.
	cmd := exec.Command(bin, "listen", "--bind", "127.0.0.1:0")
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start CLI: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	out := bufio.NewReader(stdoutPipe)
	const timeout = 5 * time.Second

	// Step 2: Wait for "ready".
	ready := ndjsonLine(t, out, timeout)
	if ready["kind"] != "ready" {
		t.Fatalf("expected ready, got %v", ready)
	}
	addr, _ := ready["address"].(string)
	if addr == "" {
		t.Fatal("ready message missing address")
	}
	t.Logf("CLI listening on %s", addr)

	// Step 3: Host sends "hello".
	writeNDJSON(t, stdinPipe, map[string]any{
		"protocol": 1, "kind": "hello", "epoch": "test-epoch-001", "seq": 1,
	})

	// Step 4: External MLLP client connects and sends ADT^A01.
	mllpConn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		t.Fatalf("dial listener: %v", err)
	}
	defer mllpConn.Close()

	adt := "MSH|^~\\&|HIS|CENTRAL|LAB|PATHOLOGY|20261117100000||ADT^A01|MSG00001|P|2.5.1\r" +
		"PID|1||12345^^^MRN||Doe^John^A||19800101|M\r"
	sendMLLP(t, mllpConn, []byte(adt))

	// Step 5: CLI emits "status" (connected) and then "message".
	// Drain status events until we get the message.
	var msgEvent map[string]any
	for i := 0; i < 5; i++ {
		ev := ndjsonLine(t, out, timeout)
		if ev["kind"] == "message" {
			msgEvent = ev
			break
		}
		t.Logf("skipping event: %v", ev["kind"])
	}
	if msgEvent == nil {
		t.Fatal("did not receive message event")
	}

	msgID, _ := msgEvent["id"].(string)
	if msgID == "" {
		t.Fatalf("message event missing id: %v", msgEvent)
	}
	hl7, _ := msgEvent["hl7"].(map[string]any)
	if hl7 == nil {
		t.Fatal("message event missing hl7 object")
	}
	if hl7["messageType"] != "ADT^A01" {
		t.Errorf("messageType: got %v", hl7["messageType"])
	}
	if hl7["controlId"] != "MSG00001" {
		t.Errorf("controlId: got %v", hl7["controlId"])
	}
	t.Logf("received message event id=%s controlId=%s", msgID, hl7["controlId"])

	// Step 6: Host sends ACK decision (AA).
	writeNDJSON(t, stdinPipe, map[string]any{
		"protocol": 1, "kind": "ack",
		"epoch": "test-epoch-001", "seq": 2,
		"id": msgID, "ackCode": "AA",
	})

	// Step 7: The external MLLP client should receive the HL7 ACK.
	ackPayload := recvMLLP(t, mllpConn, timeout)
	if len(ackPayload) == 0 {
		t.Fatal("no ACK received by external MLLP client")
	}
	t.Logf("external client received ACK (%d bytes)", len(ackPayload))

	// Step 8: Verify MSA-2 echoes the original MSH-10.
	if !strings.Contains(string(ackPayload), "MSG00001") {
		t.Errorf("ACK missing original control ID MSG00001:\n%s", ackPayload)
	}
	if !strings.Contains(string(ackPayload), "MSA|AA|MSG00001") {
		t.Errorf("ACK missing MSA|AA|MSG00001:\n%s", ackPayload)
	}

	// Also drain the "sent" event from stdout.
	for i := 0; i < 3; i++ {
		ev := ndjsonLine(t, out, timeout)
		if ev["kind"] == "sent" {
			t.Logf("sent event received for id=%v", ev["id"])
			break
		}
		t.Logf("skipping event: %v", ev["kind"])
	}

	// Step 9: Host sends shutdown.
	writeNDJSON(t, stdinPipe, map[string]any{
		"protocol": 1, "kind": "shutdown", "epoch": "test-epoch-001", "seq": 3,
	})

	// Step 10: CLI emits "stopped".
	for i := 0; i < 5; i++ {
		ev := ndjsonLine(t, out, timeout)
		if ev["kind"] == "stopped" {
			t.Log("CLI stopped cleanly")
			break
		}
		t.Logf("skipping event: %v", ev["kind"])
	}

	// Close stdin to signal EOF if shutdown wasn't processed yet.
	stdinPipe.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Logf("CLI exit: %v (may be expected)", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("CLI did not exit within 3 seconds of shutdown")
	}
}

// TestListenStdinEOFShutdown verifies that closing stdin triggers graceful shutdown.
func TestListenStdinEOFShutdown(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin, "listen", "--bind", "127.0.0.1:0")
	stdinPipe, _ := cmd.StdinPipe()
	stdoutPipe, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	out := bufio.NewReader(stdoutPipe)
	ready := ndjsonLine(t, out, 5*time.Second)
	if ready["kind"] != "ready" {
		t.Fatalf("expected ready, got %v", ready)
	}

	// Close stdin immediately (simulates host process exit).
	stdinPipe.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		// Clean exit.
	case <-time.After(5 * time.Second):
		t.Error("CLI did not exit after stdin EOF")
	}
}

// TestListenInvalidBind verifies that a wildcard bind address is rejected.
func TestListenInvalidBind(t *testing.T) {
	bin := buildCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "listen", "--bind", "0.0.0.0:2575").CombinedOutput()
	if err == nil {
		t.Fatalf("wildcard bind accepted: %s", out)
	}
}

// TestConvertEndToEnd verifies the convert command on a real HL7 file.
func TestConvertEndToEnd(t *testing.T) {
	bin := buildCLI(t)
	_, thisFile, _, _ := runtime.Caller(0)
	fixture := filepath.Join(filepath.Dir(thisFile), "..", "golden", "adt", "a01.hl7")

	out, err := exec.Command(bin, "convert", "--from", "er7", "--to", "json", "--file", fixture).Output()
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(string(out), "\n")), &m); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out)
	}
	if m["kind"] != "message" {
		t.Errorf("kind: got %v", m["kind"])
	}
	hl7, _ := m["hl7"].(map[string]any)
	if hl7["messageType"] != "ADT^A01" {
		t.Errorf("messageType: got %v", hl7["messageType"])
	}
	if hl7["raw"] == "" {
		t.Error("raw field empty")
	}
}
