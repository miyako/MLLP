package transport

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/miyako/mllp/internal/framing"
)

// conn is a single TCP/TLS connection with MLLP framing.
type conn struct {
	id         string
	peer       string
	nc         net.Conn
	tls        bool
	maxFrame   int
	ackTimeout time.Duration
	mu         sync.Mutex // guards writes
}

func newConn(nc net.Conn, maxFrame int, ackTimeout time.Duration) *conn {
	if maxFrame <= 0 {
		maxFrame = framing.DefaultMaxFrame
	}
	if ackTimeout <= 0 {
		ackTimeout = DefaultACKTimeout
	}
	// TCP keepalive where the underlying connection supports it.
	if tc, ok := nc.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(DefaultKeepalive)
	}
	_, isTLS := nc.(*tls.Conn)
	return &conn{
		id:         connID(),
		peer:       nc.RemoteAddr().String(),
		nc:         nc,
		tls:        isTLS,
		maxFrame:   maxFrame,
		ackTimeout: ackTimeout,
	}
}

func connID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "conn-" + hex.EncodeToString(b[:])
}

// readLoop reads MLLP frames from the connection and sends them to frames.
// It returns when the connection closes or ctx is cancelled.
func (c *conn) readLoop(ctx context.Context, frames chan<- Frame) error {
	dec := framing.NewDecoder(c.maxFrame)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.nc.Close()
		case <-done:
		}
	}()
	defer close(done)

	err := dec.Decode(c.nc, func(payload []byte) error {
		cp := make([]byte, len(payload))
		copy(cp, payload)
		select {
		case frames <- Frame{ConnectionID: c.id, Peer: c.peer, Payload: cp, TLS: c.tls}:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	return err
}

// send writes a single MLLP-framed payload to the connection.
func (c *conn) send(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.nc.SetWriteDeadline(time.Now().Add(c.ackTimeout)); err != nil {
		return fmt.Errorf("transport: set write deadline: %w", err)
	}
	return framing.Encode(c.nc, payload)
}
