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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/miyako/mllp/internal/codec/canonical"
	"github.com/miyako/mllp/internal/logging"
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
		return runConvert(args[1:], stdin, stdout, stderr)

	case "listen":
		return runListen(args[1:], stdin, stdout, log)

	case "connect":
		return runConnect(args[1:], stdin, stdout, log)

	case "probe":
		return runProbe(args[1:], stdout, log)

	default:
		return usageError()
	}
}

func usageError() error {
	return errors.New("usage: mllp <listen|connect|convert|probe|version>")
}

func writeVersion(stdout io.Writer) error {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{
		"protocol": 1,
		"version":  version,
	})
}

// runConvert handles offline ER7 ↔ JSON conversion (no TCP required).
func runConvert(args []string, stdin io.Reader, stdout io.Writer, _ io.Writer) error {
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

	// Normalize line endings: HL7 uses CR (0x0D); files may use LF or CRLF.
	normalized := strings.ReplaceAll(string(payload), "\r\n", "\r")
	normalized = strings.ReplaceAll(normalized, "\n", "\r")
	payload = []byte(normalized)

	msg, err := canonical.Encode(payload, canonical.Options{
		ID:           nonce(),
		ConnectionID: "offline",
		Direction:    "inbound",
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

// runListen is a stub for M1 (transport layer). Will be fully implemented when
// the TCP listener is added in the next milestone.
func runListen(args []string, _ io.Reader, stdout io.Writer, log *logging.Logger) error {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	bind := fs.String("bind", "127.0.0.1:2575", "IP:port to listen on")
	_ = fs.String("tls-cert", "", "TLS certificate file")
	_ = fs.String("tls-key", "", "TLS key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log.Info("listen: not yet implemented (M1)", map[string]any{"bind": *bind})
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{
		"protocol": 1,
		"kind":     "error",
		"code":     "not_implemented",
		"message":  "listen command will be implemented in M1",
	})
}

// runConnect is a stub for M1.
func runConnect(args []string, _ io.Reader, stdout io.Writer, log *logging.Logger) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	host := fs.String("host", "", "remote host")
	port := fs.String("port", "2575", "remote port")
	_ = fs.Bool("tls", false, "enable TLS")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log.Info("connect: not yet implemented (M1)", map[string]any{"host": *host, "port": *port})
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{
		"protocol": 1,
		"kind":     "error",
		"code":     "not_implemented",
		"message":  "connect command will be implemented in M1",
	})
}

// runProbe is a stub for M1.
func runProbe(args []string, stdout io.Writer, log *logging.Logger) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	host := fs.String("host", "", "remote host")
	port := fs.String("port", "2575", "remote port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log.Info("probe: not yet implemented (M1)", map[string]any{"host": *host, "port": *port})
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{
		"protocol": 1,
		"kind":     "error",
		"code":     "not_implemented",
		"message":  "probe command will be implemented in M1",
	})
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		enc := json.NewEncoder(os.Stderr)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(map[string]any{
			"protocol": 1,
			"kind":     "error",
			"code":     "startup_error",
			"message":  err.Error(),
		})
		os.Exit(2)
	}
}
