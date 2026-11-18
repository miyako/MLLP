package transport

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/miyako/mllp/internal/framing"
)

// dialListener dials the listener's address and returns the net.Conn.
func dialListener(t *testing.T, addr string) net.Conn {
	t.Helper()
	nc, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return nc
}

// sendFrame writes an MLLP-framed payload to nc.
func sendFrame(t *testing.T, nc net.Conn, payload []byte) {
	t.Helper()
	if err := framing.Encode(nc, payload); err != nil {
		t.Fatalf("sendFrame: %v", err)
	}
}

// recvFrame reads and decodes one MLLP frame from nc.
func recvFrame(t *testing.T, nc net.Conn) []byte {
	t.Helper()
	dec := framing.NewDecoder(framing.DefaultMaxFrame)
	var got []byte
	nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	err := dec.Decode(nc, func(payload []byte) error {
		cp := make([]byte, len(payload))
		copy(cp, payload)
		got = cp
		return io.EOF // stop after first frame
	})
	if err != nil && err != io.EOF {
		t.Fatalf("recvFrame: %v", err)
	}
	return got
}

func TestListenerAcceptsAndReceivesFrame(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l, err := NewListener(ListenConfig{Bind: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := l.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Shutdown()

	// Connect a test client and send a frame.
	nc := dialListener(t, addr)
	defer nc.Close()

	msg := []byte("MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\rPID|1||12345||Doe^John\r")
	sendFrame(t, nc, msg)

	// Expect the connection event and then the frame.
	var connEvent ConnEvent
	select {
	case connEvent = <-l.Events():
	case <-ctx.Done():
		t.Fatal("timeout waiting for connected event")
	}
	if connEvent.Event != "connected" {
		t.Fatalf("expected connected, got %q", connEvent.Event)
	}

	var frame Frame
	select {
	case frame = <-l.Frames():
	case <-ctx.Done():
		t.Fatal("timeout waiting for frame")
	}
	if string(frame.Payload) != string(msg) {
		t.Errorf("payload mismatch:\ngot:  %q\nwant: %q", frame.Payload, msg)
	}
	if frame.ConnectionID != connEvent.ConnectionID {
		t.Errorf("connection ID mismatch: frame=%q event=%q", frame.ConnectionID, connEvent.ConnectionID)
	}
}

func TestListenerSendsFrame(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l, err := NewListener(ListenConfig{Bind: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := l.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Shutdown()

	nc := dialListener(t, addr)
	defer nc.Close()

	// Wait for connection event to get the connection ID.
	var connEvent ConnEvent
	select {
	case connEvent = <-l.Events():
	case <-ctx.Done():
		t.Fatal("timeout waiting for connected event")
	}

	// Send a frame back to the client via the listener's send channel.
	ack := []byte("MSH|^~\\&|B|C|A|D|20260101||ACK|ACK001|P|2.5.1\rMSA|AA|ID1\r")
	result := make(chan error, 1)
	l.Sends() <- SendRequest{ConnectionID: connEvent.ConnectionID, Payload: ack, Result: result}

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("send error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for send result")
	}

	// Receive the frame on the client side.
	got := recvFrame(t, nc)
	if string(got) != string(ack) {
		t.Errorf("sent payload mismatch:\ngot:  %q\nwant: %q", got, ack)
	}
}

func TestListenerMultipleConcurrentConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	l, err := NewListener(ListenConfig{Bind: "127.0.0.1:0", MaxConnections: 10})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := l.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Shutdown()

	const n = 5
	clients := make([]net.Conn, n)
	for i := range clients {
		clients[i] = dialListener(t, addr)
		defer clients[i].Close()
	}

	// Send a frame from each client.
	for i, nc := range clients {
		payload := []byte("MSG-" + string(rune('A'+i)))
		sendFrame(t, nc, payload)
	}

	// Collect all frames — order is not guaranteed with concurrent connections.
	received := map[string]bool{}
	timeout := time.After(5 * time.Second)
	for len(received) < n {
		select {
		case f := <-l.Frames():
			received[string(f.Payload)] = true
		case <-timeout:
			t.Fatalf("timeout: only received %d of %d frames", len(received), n)
		}
	}
	for i := 0; i < n; i++ {
		key := "MSG-" + string(rune('A'+i))
		if !received[key] {
			t.Errorf("missing frame: %s", key)
		}
	}
}

func TestListenerConnectionLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l, err := NewListener(ListenConfig{Bind: "127.0.0.1:0", MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := l.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Shutdown()

	c1 := dialListener(t, addr)
	defer c1.Close()
	c2 := dialListener(t, addr)
	defer c2.Close()

	// Wait briefly for both connections to register.
	time.Sleep(100 * time.Millisecond)

	// Third connection should be accepted at TCP level but then closed by the listener.
	c3 := dialListener(t, addr)
	defer c3.Close()

	// The listener should emit an error event for the rejected connection.
	c3.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	_, err = c3.Read(buf)
	if err == nil {
		t.Error("expected connection to be closed by limit enforcement")
	}
}

func TestListenerDisconnectEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l, err := NewListener(ListenConfig{Bind: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := l.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Shutdown()

	nc := dialListener(t, addr)

	// Wait for connected event.
	select {
	case ev := <-l.Events():
		if ev.Event != "connected" {
			t.Fatalf("expected connected, got %q", ev.Event)
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}

	// Close the client connection.
	nc.Close()

	// Expect a disconnected event.
	select {
	case ev := <-l.Events():
		if ev.Event != "disconnected" {
			t.Fatalf("expected disconnected, got %q", ev.Event)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for disconnected event")
	}
}

func TestConnectorDialAndReceive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Set up a plain TCP echo server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverMsg := []byte("MSH|^~\\&|REMOTE|R|LOCAL|L|20260101||ORU^R01|RES001|P|2.5.1\r")

	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		// Server sends one MLLP frame.
		_ = framing.Encode(nc, serverMsg)
		// Keep connection open briefly.
		time.Sleep(500 * time.Millisecond)
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	connector, err := NewConnector(ConnectConfig{Host: host, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.Connect(ctx); err != nil {
		t.Fatal(err)
	}

	// Expect connected event.
	select {
	case ev := <-connector.Events():
		if ev.Event != "connected" {
			t.Fatalf("expected connected, got %q", ev.Event)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for connected event")
	}

	// Expect the frame from the server.
	select {
	case f := <-connector.Frames():
		if string(f.Payload) != string(serverMsg) {
			t.Errorf("payload mismatch:\ngot:  %q\nwant: %q", f.Payload, serverMsg)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for frame from server")
	}
}

func TestConnectorSend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	received := make(chan []byte, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		dec := framing.NewDecoder(framing.DefaultMaxFrame)
		_ = dec.Decode(nc, func(payload []byte) error {
			cp := make([]byte, len(payload))
			copy(cp, payload)
			received <- cp
			return io.EOF
		})
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	connector, err := NewConnector(ConnectConfig{Host: host, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.Connect(ctx); err != nil {
		t.Fatal(err)
	}

	// Wait for connected event.
	select {
	case <-connector.Events():
	case <-ctx.Done():
		t.Fatal("timeout waiting for connected event")
	}

	msg := []byte("MSH|^~\\&|A|B|C|D|20260101||ADT^A01|ID1|P|2.5.1\r")
	result := make(chan error, 1)
	connector.Sends() <- SendRequest{Payload: msg, Result: result}

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("send error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for send result")
	}

	select {
	case got := <-received:
		if string(got) != string(msg) {
			t.Errorf("server received %q, want %q", got, msg)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for server to receive frame")
	}
}

func TestBuildTLSConfigNoop(t *testing.T) {
	cfg, err := BuildTLSConfig(TLSConfig{}, true)
	if err != nil || cfg != nil {
		t.Errorf("empty TLS config should return nil, nil: %v %v", cfg, err)
	}
}

func TestBuildTLSConfigMissingKey(t *testing.T) {
	_, err := BuildTLSConfig(TLSConfig{CertFile: "cert.pem"}, true)
	if err == nil {
		t.Error("cert without key should fail")
	}
}
