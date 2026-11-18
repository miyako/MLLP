// Package transport implements TCP/TLS connection management for the MLLP bridge.
//
// It owns:
//   - The TCP listener (inbound connections)
//   - Outbound connector (persistent connection to a remote peer)
//   - Per-connection lifecycle, timeouts, and backpressure
//   - TLS configuration and mutual TLS
//
// It does NOT know about HL7, JSON, or the host protocol. Those concerns belong
// to the layers above. Transport hands raw MLLP frame payloads up and receives
// raw payloads to send down.
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultACKTimeout is how long a connection waits for the host to return an
// ACK decision before treating the outcome as unknown.
const DefaultACKTimeout = 30 * time.Second

// DefaultShutdownGrace is the maximum time allowed for in-flight messages to
// settle during graceful shutdown.
const DefaultShutdownGrace = 5 * time.Second

// DefaultMaxConnections is the default inbound connection limit.
const DefaultMaxConnections = 100

// DefaultKeepalive is the TCP keepalive interval.
const DefaultKeepalive = 30 * time.Second

// TLSConfig holds TLS parameters for a listener or connector.
type TLSConfig struct {
	// CertFile and KeyFile are paths to the PEM certificate and key.
	CertFile string
	KeyFile  string
	// CAFile is the path to a PEM CA bundle for peer certificate verification.
	// Required for mTLS on the server side; used for custom CA on the client.
	CAFile string
	// VerifyPeer enables mutual TLS (client certificate required on listener).
	VerifyPeer bool
	// MinVersion sets the minimum TLS version. Defaults to TLS 1.2.
	MinVersion uint16
	// ServerName is used by the client for SNI and certificate verification.
	ServerName string
}

// BuildTLSConfig creates a *tls.Config from TLSConfig.
// Returns nil, nil if cfg is the zero value (no TLS).
func BuildTLSConfig(cfg TLSConfig, isServer bool) (*tls.Config, error) {
	if cfg.CertFile == "" && cfg.KeyFile == "" && cfg.CAFile == "" {
		return nil, nil
	}
	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return nil, errors.New("tls: cert and key must be supplied together")
	}

	minVer := cfg.MinVersion
	if minVer == 0 {
		minVer = tls.VersionTLS12
	}

	tlsCfg := &tls.Config{
		MinVersion: minVer,
	}

	if cfg.CertFile != "" {
		pair, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("tls: load cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{pair}
	}

	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("tls: read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("tls: no valid certificates found in CA file")
		}
		if isServer {
			tlsCfg.ClientCAs = pool
			if cfg.VerifyPeer {
				tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
			}
		} else {
			tlsCfg.RootCAs = pool
		}
	}

	if !isServer && cfg.ServerName != "" {
		tlsCfg.ServerName = cfg.ServerName
	}

	return tlsCfg, nil
}

// ListenConfig holds parameters for a TCP listener.
type ListenConfig struct {
	// Bind is the address to listen on (e.g. "127.0.0.1:2575").
	// Wildcard binds are forbidden.
	Bind string
	// TLS optionally enables TLS on the listener.
	TLS TLSConfig
	// MaxConnections limits concurrent inbound connections. 0 = DefaultMaxConnections.
	MaxConnections int
	// MaxFrameBytes is the maximum MLLP frame size. 0 = framing.DefaultMaxFrame.
	MaxFrameBytes int
	// ACKTimeout is per-message ACK decision timeout. 0 = DefaultACKTimeout.
	ACKTimeout time.Duration
}

// ConnectConfig holds parameters for an outbound connection.
type ConnectConfig struct {
	// Host and Port identify the remote endpoint.
	Host string
	Port string
	// TLS optionally enables TLS for the connection.
	TLS TLSConfig
	// MaxFrameBytes is the maximum MLLP frame size. 0 = framing.DefaultMaxFrame.
	MaxFrameBytes int
	// ACKTimeout is per-message ACK decision timeout. 0 = DefaultACKTimeout.
	ACKTimeout time.Duration
	// DialTimeout limits the TCP connection attempt. 0 = 10 seconds.
	DialTimeout time.Duration
}

// ConnEvent is a connection lifecycle event.
type ConnEvent struct {
	ConnectionID string
	Peer         string
	Event        string // "connected", "disconnected", "error"
	Err          error  // non-nil for "error" and "disconnected" on error
}

// Frame is a received MLLP payload with its connection context.
type Frame struct {
	ConnectionID string
	Peer         string
	Payload      []byte
	TLS          bool // true when the connection uses TLS
}

// SendRequest asks the transport to send a payload on a specific connection.
type SendRequest struct {
	ConnectionID string
	Payload      []byte
	// Result receives nil on success or an error. Must be buffered (cap 1).
	Result chan<- error
}

// Listener manages inbound MLLP connections.
type Listener struct {
	cfg      ListenConfig
	tlsCfg   *tls.Config
	ln       net.Listener
	frames   chan Frame
	events   chan ConnEvent
	sends    chan SendRequest
	connCount atomic.Int64
	mu       sync.Mutex
	conns    map[string]*conn
	wg       sync.WaitGroup
}

// NewListener creates a Listener. Call Start to begin accepting connections.
func NewListener(cfg ListenConfig) (*Listener, error) {
	tlsCfg, err := BuildTLSConfig(cfg.TLS, true)
	if err != nil {
		return nil, err
	}
	if cfg.MaxConnections == 0 {
		cfg.MaxConnections = DefaultMaxConnections
	}
	if cfg.ACKTimeout == 0 {
		cfg.ACKTimeout = DefaultACKTimeout
	}
	return &Listener{
		cfg:    cfg,
		tlsCfg: tlsCfg,
		frames: make(chan Frame, 256),
		events: make(chan ConnEvent, 64),
		sends:  make(chan SendRequest, 256),
		conns:  make(map[string]*conn),
	}, nil
}

// Start binds the socket and begins accepting connections.
// It returns the bound address (useful when port 0 is specified).
// Cancel ctx to stop the listener.
func (l *Listener) Start(ctx context.Context) (string, error) {
	var ln net.Listener
	var err error
	if l.tlsCfg != nil {
		ln, err = tls.Listen("tcp", l.cfg.Bind, l.tlsCfg)
	} else {
		ln, err = net.Listen("tcp", l.cfg.Bind)
	}
	if err != nil {
		return "", fmt.Errorf("listen %s: %w", l.cfg.Bind, err)
	}
	l.ln = ln
	addr := ln.Addr().String()

	go l.acceptLoop(ctx)
	go l.sendLoop(ctx)
	return addr, nil
}

// Frames returns the channel on which received MLLP frame payloads arrive.
func (l *Listener) Frames() <-chan Frame { return l.frames }

// Events returns the channel on which connection lifecycle events arrive.
func (l *Listener) Events() <-chan ConnEvent { return l.events }

// Sends returns the channel into which send requests should be written.
func (l *Listener) Sends() chan<- SendRequest { return l.sends }

// Close shuts down the listener immediately (use for error paths).
func (l *Listener) Close() { _ = l.ln.Close() }

// Shutdown performs a graceful shutdown: stops accepting new connections and
// waits for all existing connections to close (up to DefaultShutdownGrace).
func (l *Listener) Shutdown() {
	_ = l.ln.Close()
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(DefaultShutdownGrace):
	}
}

func (l *Listener) acceptLoop(ctx context.Context) {
	for {
		nc, err := l.ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				l.events <- ConnEvent{Event: "error", Err: err}
			}
			return
		}
		if l.connCount.Load() >= int64(l.cfg.MaxConnections) {
			_ = nc.Close()
			l.events <- ConnEvent{Event: "error", Err: errors.New("connection limit reached")}
			continue
		}
		c := newConn(nc, l.cfg.MaxFrameBytes, l.cfg.ACKTimeout)
		l.mu.Lock()
		l.conns[c.id] = c
		l.mu.Unlock()
		l.connCount.Add(1)
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			defer l.connCount.Add(-1)
			defer func() {
				l.mu.Lock()
				delete(l.conns, c.id)
				l.mu.Unlock()
			}()
			l.events <- ConnEvent{ConnectionID: c.id, Peer: c.peer, Event: "connected"}
			err := c.readLoop(ctx, l.frames)
			l.events <- ConnEvent{ConnectionID: c.id, Peer: c.peer, Event: "disconnected", Err: err}
		}()
	}
}

func (l *Listener) sendLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-l.sends:
			l.mu.Lock()
			c, ok := l.conns[req.ConnectionID]
			l.mu.Unlock()
			if !ok {
				req.Result <- fmt.Errorf("transport: connection %s not found", req.ConnectionID)
				continue
			}
			req.Result <- c.send(req.Payload)
		}
	}
}

// Connector manages a single persistent outbound MLLP connection.
type Connector struct {
	cfg    ConnectConfig
	tlsCfg *tls.Config
	frames chan Frame
	events chan ConnEvent
	sends  chan SendRequest
}

// NewConnector creates a Connector. Call Connect to establish the connection.
func NewConnector(cfg ConnectConfig) (*Connector, error) {
	tlsCfg, err := BuildTLSConfig(cfg.TLS, false)
	if err != nil {
		return nil, err
	}
	if cfg.ACKTimeout == 0 {
		cfg.ACKTimeout = DefaultACKTimeout
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	return &Connector{
		cfg:    cfg,
		tlsCfg: tlsCfg,
		frames: make(chan Frame, 256),
		events: make(chan ConnEvent, 64),
		sends:  make(chan SendRequest, 256),
	}, nil
}

// Connect dials the remote endpoint and starts the read/send loops.
// Cancel ctx to disconnect.
func (c *Connector) Connect(ctx context.Context) error {
	addr := net.JoinHostPort(c.cfg.Host, c.cfg.Port)
	dialCtx, cancel := context.WithTimeout(ctx, c.cfg.DialTimeout)
	defer cancel()

	var nc net.Conn
	var err error
	if c.tlsCfg != nil {
		dialer := &tls.Dialer{Config: c.tlsCfg}
		nc, err = dialer.DialContext(dialCtx, "tcp", addr)
	} else {
		d := &net.Dialer{}
		nc, err = d.DialContext(dialCtx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}

	conn := newConn(nc, c.cfg.MaxFrameBytes, c.cfg.ACKTimeout)
	c.events <- ConnEvent{ConnectionID: conn.id, Peer: conn.peer, Event: "connected"}

	go func() {
		err := conn.readLoop(ctx, c.frames)
		c.events <- ConnEvent{ConnectionID: conn.id, Peer: conn.peer, Event: "disconnected", Err: err}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				_ = nc.Close()
				return
			case req := <-c.sends:
				req.Result <- conn.send(req.Payload)
			}
		}
	}()
	return nil
}

// Frames returns received MLLP frame payloads.
func (c *Connector) Frames() <-chan Frame { return c.frames }

// Events returns connection lifecycle events.
func (c *Connector) Events() <-chan ConnEvent { return c.events }

// Sends returns the channel into which send requests should be written.
func (c *Connector) Sends() chan<- SendRequest { return c.sends }
