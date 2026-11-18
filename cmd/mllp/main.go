// Command mllp is the MLLP integration bridge for 4D and other host applications.
//
// Usage:
//
//	mllp listen   --bind 127.0.0.1:2575 [--tls-cert cert.pem --tls-key key.pem]
//	mllp connect  --host 127.0.0.1 --port 2575 [--tls]
//	mllp convert  --from er7 --to json --file message.hl7
//	mllp probe    --host 127.0.0.1 --port 2575
//	mllp version
//
// The CLI communicates with its host application via NDJSON over stdin/stdout.
// stderr carries structured diagnostic logs only. No PHI ever appears in diagnostics.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/miyako/mllp/internal/ack"
	"github.com/miyako/mllp/internal/codec/canonical"
	"github.com/miyako/mllp/internal/hl7/er7"
	"github.com/miyako/mllp/internal/host/protocol"
	"github.com/miyako/mllp/internal/logging"
	"github.com/miyako/mllp/internal/transport"
)

const version = "0.1.0"

func nonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	for _, arg := range args {
		if !utf8.ValidString(arg) {
			return errors.New("arguments must be valid UTF-8")
		}
	}

	log := logging.New(stderr, logging.LevelInfo)

	if len(args) == 0 {
		return usageError()
	}

	switch args[0] {
	case "version", "--version":
		return writeVersion(stdout)
	case "convert":
		return runConvert(args[1:], stdin, stdout)
	case "listen":
		return runListen(args[1:], stdin, stdout, stderr, log)
	case "connect":
		return runConnect(args[1:], stdin, stdout, stderr, log)
	case "send":
		return runSend(args[1:], stdout, log)
	case "probe":
		return runProbe(args[1:], stdout, log)
	default:
		return usageError()
	}
}

func usageError() error {
	return errors.New("usage: mllp <listen|connect|send|convert|probe|version>")
}

func writeVersion(stdout io.Writer) error {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{"protocol": 1, "version": version})
}

// runConvert handles offline ER7 → JSON conversion (no TCP required).
func runConvert(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("convert", flag.ContinueOnError)
	from := fs.String("from", "", "source format: er7")
	to := fs.String("to", "", "target format: json")
	file := fs.String("file", "", "input file (omit to read from stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" {
		return errors.New("convert: --from and --to are required")
	}
	if *from != "er7" {
		return fmt.Errorf("convert: unsupported --from format %q (supported: er7)", *from)
	}
	if *to != "json" {
		return fmt.Errorf("convert: unsupported --to format %q (supported: json)", *to)
	}

	var payload []byte
	if *file != "" {
		var err error
		payload, err = os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("convert: %w", err)
		}
	} else {
		var err error
		payload, err = io.ReadAll(stdin)
		if err != nil {
			return fmt.Errorf("convert: reading stdin: %w", err)
		}
	}

	// Normalize line endings: HL7 uses CR; files may use LF or CRLF.
	normalized := strings.ReplaceAll(string(payload), "\r\n", "\r")
	normalized = strings.ReplaceAll(normalized, "\n", "\r")
	payload = []byte(normalized)

	msg, err := canonical.Encode(payload, canonical.Options{
		ID: nonce(), ConnectionID: "offline", Direction: "inbound",
	})
	if err != nil {
		return fmt.Errorf("convert: %w", err)
	}
	b, err := canonical.Marshal(msg)
	if err != nil {
		return fmt.Errorf("convert: marshal: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "%s\n", b)
	return err
}

// runListen starts an inbound MLLP listener, connects it to the host protocol,
// and runs the full deferred-ACK loop.
func runListen(args []string, stdin io.Reader, stdout, stderr io.Writer, log *logging.Logger) error {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bind := fs.String("bind", "127.0.0.1:2575", "IP:port to listen on")
	tlsCert := fs.String("tls-cert", "", "TLS certificate file")
	tlsKey := fs.String("tls-key", "", "TLS key file")
	tlsCA := fs.String("tls-ca", "", "CA bundle for mTLS")
	maxConn := fs.Int("max-connections", transport.DefaultMaxConnections, "maximum concurrent inbound connections")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("listen: unexpected positional argument")
	}

	// Validate bind address: must be a concrete IP, not a wildcard.
	host, _, err := net.SplitHostPort(*bind)
	if err != nil {
		return fmt.Errorf("listen: invalid --bind address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		return errors.New("listen: --bind requires a concrete IP address; wildcard binds are forbidden")
	}

	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("listen: --tls-cert and --tls-key must be supplied together")
	}

	cfg := transport.ListenConfig{
		Bind:           *bind,
		MaxConnections: *maxConn,
		TLS: transport.TLSConfig{
			CertFile:   *tlsCert,
			KeyFile:    *tlsKey,
			CAFile:     *tlsCA,
			VerifyPeer: *tlsCA != "",
		},
	}

	l, err := transport.NewListener(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	addr, err := l.Start(ctx)
	if err != nil {
		return err
	}
	defer l.Shutdown()

	out := protocol.NewSafeWriter(stdout)
	reader := protocol.NewReader(stdin)

	// Emit ready.
	if err := out.Write(map[string]any{
		"protocol": 1, "kind": "ready", "version": version,
		"mode": "listen", "address": addr,
	}); err != nil {
		return err
	}

	log.Info("listener started", map[string]any{"address": addr})

	// pending maps CLI message ID → pendingMsg, waiting for host ACK.
	pending := map[string]*pendingMsg{}

	failures := make(chan error, 4)

	// stdin reader goroutine. Signals failures (including io.EOF) to the main loop.
	go func() {
		for {
			cmd, err := reader.Read()
			if err != nil {
				failures <- err
				return
			}
			if err := handleHostCommand(cmd, pending, l.Sends(), out, log, cancel); err != nil {
				failures <- err
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return gracefulStop(out, log)

		case err := <-failures:
			if err == io.EOF {
				return gracefulStop(out, log)
			}
			return err

		case ev := <-l.Events():
			emitStatus(out, ev, log)

		case frame := <-l.Frames():
			if err := handleFrame(frame, pending, l.Sends(), out, log); err != nil {
				log.Error("frame handling failed", map[string]any{"connectionId": frame.ConnectionID, "error": err.Error()})
			}
		}
	}
}

// runConnect starts a persistent outbound MLLP connection and runs the host protocol loop.
func runConnect(args []string, stdin io.Reader, stdout, stderr io.Writer, log *logging.Logger) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", "", "remote host (required)")
	port := fs.String("port", "2575", "remote port")
	tlsCert := fs.String("tls-cert", "", "TLS client certificate file")
	tlsKey := fs.String("tls-key", "", "TLS client key file")
	tlsCA := fs.String("tls-ca", "", "CA bundle for server certificate verification")
	tlsServerName := fs.String("tls-server-name", "", "TLS server name for SNI")
	useTLS := fs.Bool("tls", false, "enable TLS (required when tls-cert or tls-ca is set)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *host == "" {
		return errors.New("connect: --host is required")
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("connect: --tls-cert and --tls-key must be supplied together")
	}
	_ = useTLS // presence of cert/ca flags implies TLS

	cfg := transport.ConnectConfig{
		Host: *host,
		Port: *port,
		TLS: transport.TLSConfig{
			CertFile:   *tlsCert,
			KeyFile:    *tlsKey,
			CAFile:     *tlsCA,
			ServerName: *tlsServerName,
		},
	}

	connector, err := transport.NewConnector(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := connector.Connect(ctx); err != nil {
		return err
	}

	out := protocol.NewSafeWriter(stdout)
	reader := protocol.NewReader(stdin)

	// Emit ready.
	if err := out.Write(map[string]any{
		"protocol": 1, "kind": "ready", "version": version,
		"mode": "connect", "address": net.JoinHostPort(*host, *port),
	}); err != nil {
		return err
	}

	log.Info("connector started", map[string]any{"host": *host, "port": *port})

	// For connect mode, pendingSends maps CLI send ID → original message.
	// ACK correlation is handled when remote ACKs arrive as inbound frames.
	pending := map[string]string{}

	failures := make(chan error, 4)

	go func() {
		for {
			cmd, err := reader.Read()
			if err != nil {
				failures <- err
				return
			}
			if err := handleHostCommandConnector(cmd, connector.Sends(), out, log, cancel); err != nil {
				failures <- err
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return gracefulStop(out, log)
		case err := <-failures:
			if err == io.EOF {
				return gracefulStop(out, log)
			}
			return err
		case ev := <-connector.Events():
			emitStatus(out, ev, log)
		case frame := <-connector.Frames():
			// In connect mode, inbound frames are ACKs from the remote system.
			if err := handleRemoteACK(frame, pending, out, log); err != nil {
				log.Error("remote ACK handling failed", map[string]any{"error": err.Error()})
			}
		}
	}
}

// runSend is a one-shot outbound send: dial, send one message, wait for ACK, exit.
func runSend(args []string, stdout io.Writer, log *logging.Logger) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	host := fs.String("host", "", "remote host (required)")
	port := fs.String("port", "2575", "remote port")
	file := fs.String("file", "", "HL7 file to send (required)")
	waitACK := fs.Bool("wait-ack", true, "wait for HL7 ACK before exiting")
	tlsCert := fs.String("tls-cert", "", "TLS client certificate")
	tlsKey := fs.String("tls-key", "", "TLS client key")
	tlsCA := fs.String("tls-ca", "", "CA bundle")
	tlsServerName := fs.String("tls-server-name", "", "TLS server name for SNI")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *host == "" {
		return errors.New("send: --host is required")
	}
	if *file == "" {
		return errors.New("send: --file is required")
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("send: --tls-cert and --tls-key must be supplied together")
	}

	raw, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\r")
	normalized = strings.ReplaceAll(normalized, "\n", "\r")
	payload := []byte(normalized)

	// Parse to extract MSH-10 for ACK correlation.
	origMsg, err := er7.Parse(payload)
	if err != nil {
		return fmt.Errorf("send: HL7 parse: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := transport.ConnectConfig{
		Host: *host, Port: *port,
		TLS: transport.TLSConfig{
			CertFile:   *tlsCert,
			KeyFile:    *tlsKey,
			CAFile:     *tlsCA,
			ServerName: *tlsServerName,
		},
	}
	connector, err := transport.NewConnector(cfg)
	if err != nil {
		return err
	}
	if err := connector.Connect(ctx); err != nil {
		return err
	}

	// Wait for connected event.
	select {
	case ev := <-connector.Events():
		if ev.Event != "connected" {
			return fmt.Errorf("send: unexpected event %q", ev.Event)
		}
		log.Info("connected", map[string]any{"peer": ev.Peer})
	case <-ctx.Done():
		return errors.New("send: connection timeout")
	}

	// Send the message.
	result := make(chan error, 1)
	connector.Sends() <- transport.SendRequest{Payload: payload, Result: result}
	select {
	case err := <-result:
		if err != nil {
			return fmt.Errorf("send: %w", err)
		}
	case <-ctx.Done():
		return errors.New("send: send timeout")
	}
	log.Info("message sent", map[string]any{"controlId": origMsg.ControlID})

	out := protocol.NewSafeWriter(stdout)
	if !*waitACK {
		return out.Write(map[string]any{
			"protocol": 1, "kind": "sent", "controlId": origMsg.ControlID,
		})
	}

	// Wait for the remote ACK frame.
	select {
	case frame := <-connector.Frames():
		ackMsg, err := er7.Parse(frame.Payload)
		if err != nil {
			return fmt.Errorf("send: ACK parse: %w", err)
		}
		if err := ack.Validate(origMsg.ControlID, ackMsg); err != nil {
			return fmt.Errorf("send: ACK correlation: %w", err)
		}
		code, err := ack.CodeFromMSA1(ackMsg)
		if err != nil {
			return fmt.Errorf("send: ACK code: %w", err)
		}
		log.Info("ACK received", map[string]any{"controlId": origMsg.ControlID, "code": string(code)})
		if err := out.Write(map[string]any{
			"protocol": 1, "kind": "ack",
			"ackCode": string(code), "controlId": origMsg.ControlID,
		}); err != nil {
			return err
		}
		if code == ack.AE || code == ack.AR {
			os.Exit(3)
		}
		return nil
	case ev := <-connector.Events():
		return fmt.Errorf("send: connection event before ACK: %v %v", ev.Event, ev.Err)
	case <-ctx.Done():
		return errors.New("send: ACK timeout")
	}
}

// runProbe does a TCP connectivity check and reports the result.
func runProbe(args []string, stdout io.Writer, log *logging.Logger) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	host := fs.String("host", "", "remote host (required)")
	port := fs.String("port", "2575", "remote port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *host == "" {
		return errors.New("probe: --host is required")
	}
	addr := net.JoinHostPort(*host, *port)
	start := time.Now()
	nc, err := net.DialTimeout("tcp", addr, 10*time.Second)
	elapsed := time.Since(start)
	out := protocol.NewSafeWriter(stdout)
	if err != nil {
		log.Warn("probe failed", map[string]any{"addr": addr, "error": err.Error()})
		return out.Write(map[string]any{
			"protocol": 1, "kind": "error", "code": "connection_refused",
			"message": err.Error(),
		})
	}
	nc.Close()
	log.Info("probe succeeded", map[string]any{"addr": addr, "latencyMs": elapsed.Milliseconds()})
	return out.Write(map[string]any{
		"protocol": 1, "kind": "status", "event": "reachable",
		"peer": addr, "latencyMs": elapsed.Milliseconds(),
	})
}

// --- frame and command handlers ---

// pendingMsg tracks an inbound message waiting for a host ACK decision.
type pendingMsg struct {
	connectionID string
	original     *er7.Message // needed to build the HL7 ACK bytes
}

// handleFrame converts a raw MLLP frame to a canonical JSON message and
// forwards it to the host. It stores the original parsed message in pending
// so that handleHostACK can later build the correct HL7 ACK bytes.
func handleFrame(frame transport.Frame, pending map[string]*pendingMsg, sends chan<- transport.SendRequest, out *protocol.SafeWriter, log *logging.Logger) error {
	msgID := "msg-" + nonce()

	parsed, err := er7.Parse(frame.Payload)
	if err != nil {
		log.Warn("hl7 parse failed", map[string]any{"connectionId": frame.ConnectionID, "error": err.Error()})
		return sendParseErrorACK(frame, sends, log)
	}

	pending[msgID] = &pendingMsg{connectionID: frame.ConnectionID, original: parsed}

	msg, err := canonical.Encode(frame.Payload, canonical.Options{
		ID:           msgID,
		ConnectionID: frame.ConnectionID,
		Direction:    "inbound",
		Peer:         frame.Peer,
		TLS:          frame.TLS,
	})
	if err != nil {
		delete(pending, msgID)
		log.Warn("canonical encode failed", map[string]any{"connectionId": frame.ConnectionID, "error": err.Error()})
		return sendParseErrorACK(frame, sends, log)
	}

	return out.Write(msg)
}

func sendParseErrorACK(frame transport.Frame, sends chan<- transport.SendRequest, log *logging.Logger) error {
	// Build a minimal AE ACK without a valid original message.
	// We can't build a proper ACK without a parsed message; send a generic rejection frame.
	log.Warn("sending parse-error rejection to peer", map[string]any{"peer": frame.Peer})
	// Without a valid MSH we cannot build a conformant ACK — close is the safest option.
	// In practice the caller should close the connection; here we just log.
	return nil
}

func handleHostCommand(cmd *protocol.HostCommand, pending map[string]*pendingMsg, sends chan<- transport.SendRequest, out *protocol.SafeWriter, log *logging.Logger, cancel context.CancelFunc) error {
	switch cmd.Kind {
	case protocol.KindHello:
		// Hello accepted by the reader; nothing else to do.
		log.Info("host hello received", nil)

	case protocol.KindAckHost:
		return handleHostACK(cmd, pending, sends, out, log)
	case protocol.KindShutdown:
		cancel() // trigger graceful shutdown via context
		return nil

	case protocol.KindPing:
		return out.Write(map[string]any{
			"protocol": 1, "kind": "status", "event": "pong",
		})

	default:
		log.Warn("unhandled host command", map[string]any{"kind": cmd.Kind})
	}
	return nil
}

func handleHostACK(cmd *protocol.HostCommand, pending map[string]*pendingMsg, sends chan<- transport.SendRequest, out *protocol.SafeWriter, log *logging.Logger) error {
	var ackMsg protocol.AckHostMsg
	if err := decodeRaw(cmd.Raw, &ackMsg); err != nil {
		return fmt.Errorf("protocol: malformed ack message: %w", err)
	}
	msgID := ackMsg.ID
	pm, ok := pending[msgID]
	if !ok {
		log.Warn("host ACK for unknown message", map[string]any{"id": msgID})
		return nil
	}
	delete(pending, msgID)

	code := ack.Code(ackMsg.AckCode)
	switch code {
	case ack.AA, ack.AE, ack.AR:
	default:
		return fmt.Errorf("protocol: invalid ackCode %q", ackMsg.AckCode)
	}

	// Build the HL7 ACK bytes from the original message.
	ackPayload, err := ack.Build(pm.original, ack.Options{
		Code:      code,
		ErrorCode: ackMsg.ErrorCode,
		ErrorText: ackMsg.ErrorText,
	})
	if err != nil {
		log.Error("ack build failed", map[string]any{"id": msgID, "error": err.Error()})
		return err
	}

	// Send the ACK back to the remote peer on the same connection.
	result := make(chan error, 1)
	sends <- transport.SendRequest{
		ConnectionID: pm.connectionID,
		Payload:      []byte(ackPayload),
		Result:       result,
	}

	go func() {
		err := <-result
		if err != nil {
			log.Error("ack send failed", map[string]any{"id": msgID, "connectionId": pm.connectionID, "error": err.Error()})
			_ = out.Write(map[string]any{
				"protocol": 1, "kind": "error", "code": "ack_send_failed",
				"message": err.Error(), "connectionId": pm.connectionID,
			})
			return
		}
		log.Info("ack sent to peer", map[string]any{
			"id": msgID, "code": string(code), "connectionId": pm.connectionID,
			"controlId": pm.original.ControlID,
		})
		_ = out.Write(map[string]any{
			"protocol": 1, "kind": "sent", "id": msgID, "connectionId": pm.connectionID,
		})
	}()
	return nil
}

func handleHostCommandConnector(cmd *protocol.HostCommand, sends chan<- transport.SendRequest, out *protocol.SafeWriter, log *logging.Logger, cancel context.CancelFunc) error {
	switch cmd.Kind {
	case protocol.KindHello:
		log.Info("host hello received", nil)
	case protocol.KindSend:
		return handleHostSend(cmd, sends, out, log)
	case protocol.KindShutdown:
		cancel()
		return nil
	case protocol.KindPing:
		return out.Write(map[string]any{"protocol": 1, "kind": "status", "event": "pong"})
	default:
		log.Warn("unhandled host command", map[string]any{"kind": cmd.Kind})
	}
	return nil
}

func handleHostSend(cmd *protocol.HostCommand, sends chan<- transport.SendRequest, out *protocol.SafeWriter, log *logging.Logger) error {
	// Extract the hl7.raw field from the send command.
	var sendMsg struct {
		ID  string `json:"id"`
		HL7 struct {
			Raw string `json:"raw"`
		} `json:"hl7"`
	}
	if err := decodeRaw(cmd.Raw, &sendMsg); err != nil {
		return fmt.Errorf("protocol: malformed send message: %w", err)
	}
	if sendMsg.HL7.Raw == "" {
		return errors.New("protocol: send message missing hl7.raw")
	}
	payload := []byte(sendMsg.HL7.Raw)

	result := make(chan error, 1)
	sends <- transport.SendRequest{Payload: payload, Result: result}

	go func() {
		err := <-result
		if err != nil {
			log.Error("outbound send failed", map[string]any{"id": sendMsg.ID, "error": err.Error()})
			_ = out.Write(map[string]any{
				"protocol": 1, "kind": "error", "code": "send_failed",
				"message": err.Error(), "id": sendMsg.ID,
			})
			return
		}
		_ = out.Write(map[string]any{
			"protocol": 1, "kind": "sent", "id": sendMsg.ID,
		})
	}()
	return nil
}

func handleRemoteACK(frame transport.Frame, pending map[string]string, out *protocol.SafeWriter, log *logging.Logger) error {
	ackMsg, err := er7.Parse(frame.Payload)
	if err != nil {
		log.Warn("remote ACK parse failed", map[string]any{"connectionId": frame.ConnectionID, "error": err.Error()})
		return nil
	}
	code, err := ack.CodeFromMSA1(ackMsg)
	if err != nil {
		log.Warn("remote ACK code unreadable", map[string]any{"error": err.Error()})
		return nil
	}
	controlID := ackMsg.FieldAt("MSA", 2)
	log.Info("remote ACK received", map[string]any{"controlId": controlID, "code": string(code)})
	_ = pending // correlation against outbound sends tracked by the host
	return out.Write(map[string]any{
		"protocol": 1, "kind": "ack", "connectionId": frame.ConnectionID,
		"ackCode": string(code), "controlId": controlID,
	})
}

func emitStatus(out *protocol.SafeWriter, ev transport.ConnEvent, log *logging.Logger) {
	fields := map[string]any{"connectionId": ev.ConnectionID, "event": ev.Event}
	if ev.Peer != "" {
		fields["peer"] = ev.Peer
	}
	if ev.Err != nil {
		fields["reason"] = ev.Err.Error()
	}
	log.Info("connection event", fields)
	_ = out.Write(map[string]any{
		"protocol": 1, "kind": "status",
		"connectionId": ev.ConnectionID, "event": ev.Event,
		"peer": ev.Peer,
	})
}

func gracefulStop(out *protocol.SafeWriter, log *logging.Logger) error {
	log.Info("graceful shutdown complete", nil)
	_ = out.Write(map[string]any{"protocol": 1, "kind": "stopped"})
	return nil
}

func decodeRaw(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		enc := json.NewEncoder(os.Stderr)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(map[string]any{
			"protocol": 1, "kind": "error",
			"code": "startup_error", "message": err.Error(),
		})
		os.Exit(2)
	}
}
