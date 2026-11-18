# mllp

A portable, self-contained MLLP client/server CLI that lets 4D and other host applications exchange HL7 v2 messages over TCP/IP. JSON is the application-facing data abstraction; all network operations are owned by the CLI.

**Current milestone:** M5 (config, charset) — M6 (4D component) in progress.

## Architecture

```
Host application          stdin/stdout           mllp CLI              TCP/TLS
(4D SystemWorker)  ←── NDJSON Protocol 1 ───→  ┌──────────┐  ←───→  Remote HL7 system
                                                 │ listen   │
                                                 │ connect  │
                                                 │ send     │
                                                 │ convert  │
                                                 │ probe    │
                                                 └──────────┘
```

The CLI owns all TCP/MLLP framing and TLS. The host owns business logic, persistence, and ACK decisions. No TCP socket code in 4D.

Six independent layers, each with one responsibility:

| Layer | Package | Concern |
|-------|---------|---------|
| 1 | `internal/framing` | VT/FS/CR byte framing, streaming state machine |
| 2 | `internal/transport` | TCP/TLS listener and outbound connector |
| 3 | `internal/hl7/er7` | HL7 v2 ER7 parser, lossless |
| 4 | `internal/codec/canonical` | Canonical JSON representation |
| 4 | `internal/ack` | ACK generation and correlation |
| 5 | `internal/host/protocol` | NDJSON host protocol, versioned |
| 6 | `host-4d/` | 4D SystemWorker component (M6) |

## Installation

Download a signed release binary (macOS universal or Windows x64) from the Releases page. No runtime installation required.

To build from source:

```sh
go build -o mllp ./cmd/mllp
```

Requires Go 1.23+. No CGO.

## Usage

### Inbound listener (receive HL7 from remote systems)

```sh
mllp listen --bind 127.0.0.1:2575 \
  --tls-cert server.crt --tls-key server.key
```

### Outbound persistent connection

```sh
mllp connect --host 192.168.1.100 --port 2575 --tls
```

### One-shot send

```sh
mllp send --host 192.168.1.100 --port 2575 \
  --file message.hl7 --wait-ack
```

### Offline conversion (ER7 → canonical JSON)

```sh
mllp convert --from er7 --to json --file message.hl7
```

### Connectivity probe

```sh
mllp probe --host 192.168.1.100 --port 2575
```

### Configuration file

```sh
mllp --config mllp.toml listen
```

See `mllp.example.toml` for a fully-commented reference configuration.

## Host protocol

The CLI communicates with its host via NDJSON over stdin/stdout. Every line is a single UTF-8 JSON object terminated by LF. stderr carries structured JSON diagnostic logs only — no PHI, no raw payloads.

See [`docs/protocol/protocol-1.md`](docs/protocol/protocol-1.md) for the full Protocol 1 specification.

**Deferred ACK (default):**

1. External system sends HL7 → CLI receives MLLP frame
2. CLI emits `{"kind":"message","id":"msg-…","hl7":{…}}` on stdout
3. Host persists the message durably
4. Host sends `{"kind":"ack","id":"msg-…","ackCode":"AA"}` on stdin
5. CLI builds and sends the HL7 ACK bytes back to the remote peer
6. CLI emits `{"kind":"sent","id":"msg-…"}` on stdout

The CLI never manufactures an AA if the host crashes or times out.

## Security

- TLS 1.2 minimum; TLS 1.3 preferred
- Mutual TLS (mTLS) supported via `--tls-ca` + `verify_peer`
- Default bind is loopback; non-loopback requires explicit `--bind` and TLS
- No PHI in stderr logs, no raw payloads in diagnostics
- Bounded buffers and connection limits; no unbounded queues

## Development

```sh
go test ./...                       # all unit + integration tests
go test ./internal/framing/ -fuzz=FuzzDecoder -fuzztime=60s
go vet ./...
```

Tests require no external dependencies. Integration tests build the CLI binary into a temp directory and exercise it as a child process.

## Milestones

| # | Description | Status |
|---|-------------|--------|
| M0 | Language, framing, HL7 parser, codec, ACK, host protocol | ✅ |
| M1 | TCP/TLS transport, listen/connect/probe commands | ✅ |
| M2 | Real HL7 ACK bytes sent after host decision | ✅ |
| M3 | `mllp send` one-shot command | ✅ |
| M4 | TLS integration tests, fuzz tests, VT bug fix | ✅ |
| M5 | TOML config, charset module, `--config` flag | ✅ |
| M6 | 4D SystemWorker component | 🔄 |
| M7 | FHIR R4 mappings | — |
| M8 | Hardening, interop, signed release | — |

## License

MIT — see [LICENSE](LICENSE).
