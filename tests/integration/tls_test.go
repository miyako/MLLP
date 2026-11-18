package integration

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/miyako/mllp/internal/framing"
	"github.com/miyako/mllp/internal/hl7/er7"

	mllpack "github.com/miyako/mllp/internal/ack"
)

// generateSelfSigned creates a self-signed TLS certificate for 127.0.0.1
// and writes cert.pem and key.pem into dir.
func generateSelfSigned(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mllp-test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:         true,
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certFile = dir + "/cert.pem"
	keyFile = dir + "/key.pem"

	cf, err := os.Create(certFile)
	if err != nil {
		t.Fatal(err)
	}
	_ = pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	cf.Close()

	kf, err := os.Create(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	_ = pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	kf.Close()

	return certFile, keyFile
}

// TestListenTLS verifies that the CLI listener accepts TLS connections and
// the external MLLP client (using the self-signed CA) can send a message
// and receive a proper AA ACK.
func TestListenTLS(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()
	certFile, keyFile := generateSelfSigned(t, dir)

	ctx_cancel := make(chan struct{})

	// Launch CLI with TLS.
	cmd := exec.Command(bin, "listen", "--bind", "127.0.0.1:0",
		"--tls-cert", certFile, "--tls-key", keyFile)
	stdinPipe, _ := cmd.StdinPipe()
	stdoutPipe, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(ctx_cancel)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	out := bufio.NewReader(stdoutPipe)
	const timeout = 5 * time.Second

	ready := ndjsonLine(t, out, timeout)
	if ready["kind"] != "ready" {
		t.Fatalf("expected ready, got %v", ready)
	}
	addr := ready["address"].(string)
	t.Logf("TLS listener at %s", addr)

	// Host hello.
	writeNDJSON(t, stdinPipe, map[string]any{
		"protocol": 1, "kind": "hello", "epoch": "tls-epoch-001", "seq": 1,
	})

	// Build a TLS client config trusting the self-signed cert.
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	tlsCfg := &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}

	mllpConn, err := tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", addr, tlsCfg)
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	defer mllpConn.Close()

	// Send ADT message.
	adt := "MSH|^~\\&|HIS|CENTRAL|LAB|PATH|20261117100000||ADT^A01|TLSMSG001|P|2.5.1\rPID|1||55555||TLS^Test\r"
	sendMLLP(t, mllpConn, []byte(adt))

	// Drain until message event.
	var msgEvent map[string]any
	for i := 0; i < 5; i++ {
		ev := ndjsonLine(t, out, timeout)
		if ev["kind"] == "message" {
			msgEvent = ev
			break
		}
	}
	if msgEvent == nil {
		t.Fatal("no message event received")
	}
	msgID := msgEvent["id"].(string)
	hl7, _ := msgEvent["hl7"].(map[string]any)
	if hl7["controlId"] != "TLSMSG001" {
		t.Errorf("controlId: got %v", hl7["controlId"])
	}
	// Verify transport.tls is true.
	transport, _ := msgEvent["transport"].(map[string]any)
	if transport["tls"] != true {
		t.Errorf("transport.tls should be true, got %v", transport["tls"])
	}

	// Host sends AA.
	writeNDJSON(t, stdinPipe, map[string]any{
		"protocol": 1, "kind": "ack",
		"epoch": "tls-epoch-001", "seq": 2,
		"id": msgID, "ackCode": "AA",
	})

	// Wait for the HL7 ACK on the TLS connection.
	ackPayload := recvMLLP(t, mllpConn, timeout)
	if !strings.Contains(string(ackPayload), "TLSMSG001") {
		t.Errorf("ACK missing control ID:\n%s", ackPayload)
	}

	// Shutdown.
	writeNDJSON(t, stdinPipe, map[string]any{
		"protocol": 1, "kind": "shutdown", "epoch": "tls-epoch-001", "seq": 3,
	})
	for i := 0; i < 5; i++ {
		ev := ndjsonLine(t, out, timeout)
		if ev["kind"] == "stopped" {
			break
		}
	}
}

// TestSendTLS verifies that `mllp send` can connect to a TLS server.
func TestSendTLS(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()
	certFile, keyFile := generateSelfSigned(t, dir)

	// Self-signed CA cert for client verification.
	certPEM, _ := os.ReadFile(certFile)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)

	// TLS server.
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
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
		dec := framing.NewDecoder(framing.DefaultMaxFrame)
		var received []byte
		_ = dec.Decode(nc, func(payload []byte) error {
			cp := make([]byte, len(payload))
			copy(cp, payload)
			received = cp
			return io.EOF // stop after first frame
		})
		serverReceived <- received

		msg, err := er7.Parse(received)
		if err != nil || msg.ControlID == "" {
			return
		}
		ackPayload, err := mllpack.Build(msg, mllpack.Options{
			Code: mllpack.AA, ACKControlID: "TLSACK001",
		})
		if err != nil {
			return
		}
		nc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = framing.Encode(nc, []byte(ackPayload))
		time.Sleep(300 * time.Millisecond)
	}()

	raw := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|TLSSEND001|P|2.5.1\rPID|1||111||TLS^Send\r"
	f, err := os.CreateTemp("", "mllp-tls-*.hl7")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString(raw)
	f.Close()

	host, port, _ := net.SplitHostPort(ln.Addr().String())

	done := make(chan error, 1)
	var outBuf strings.Builder
	cmd := exec.Command(bin, "send",
		"--host", host, "--port", port,
		"--file", f.Name(), "--wait-ack",
		"--tls-cert", certFile, "--tls-key", keyFile,
		"--tls-ca", certFile,
		"--tls-server-name", "127.0.0.1")
	cmd.Stdout = &outBuf
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("mllp send (TLS) failed: %v\noutput: %s", err, outBuf.String())
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("mllp send (TLS) timed out")
	}

	output := strings.TrimSpace(outBuf.String())
	var m map[string]any
	if err := json.Unmarshal([]byte(output), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%q", err, output)
	}
	if m["ackCode"] != "AA" {
		t.Errorf("ackCode: got %v want AA", m["ackCode"])
	}

	select {
	case received := <-serverReceived:
		if !strings.Contains(string(received), "TLSSEND001") {
			t.Errorf("server got wrong message: %s", received)
		}
	case <-time.After(5 * time.Second):
		t.Error("server did not receive message")
	}
}
