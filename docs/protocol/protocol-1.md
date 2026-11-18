# Host Protocol Version 1

The mllp CLI communicates with its host application (4D or any process manager) via NDJSON over standard streams.

**This document is the authoritative specification for Protocol 1.**  
No implementation may deviate from it without bumping the protocol version.

## Stream responsibilities

| Stream | Purpose |
|--------|---------|
| `stdin` | Host commands and ACK decisions |
| `stdout` | Structured CLI events and inbound HL7 messages |
| `stderr` | Diagnostic logs only — structured JSON, no PHI, no payloads |

`stdout` must never contain human-readable text, banners, progress indicators, or debug output. Every line is a single UTF-8 JSON object terminated by LF (`\n`).

## Frame constraints

| Parameter | Value |
|-----------|-------|
| Maximum stdin frame | 4 MiB |
| Maximum stdout frame | 4 MiB |
| Encoding | UTF-8, no BOM |
| Terminator | LF (`\n`) |

An incomplete frame at EOF is a fatal protocol error.

## Common envelope

Every message, in both directions, carries:

```json
{
  "protocol": 1,
  "kind": "<kind>",
  "id": "<optional-id>"
}
```

Host-to-CLI messages additionally require:

```json
{
  "epoch": "<host-process-instance-uuid>",
  "seq": 42
}
```

- `epoch` is a stable identifier for the current host process instance. It resets on every host restart. It must match `[A-Za-z0-9_.:-]{1,96}`.
- `seq` is a strictly increasing uint64, starting at 1 for the `hello` message.

## Startup sequence

1. Host launches the CLI process.
2. CLI begins listening or connecting (depending on command).
3. CLI emits `ready` on stdout when the socket is operational.
4. Host sends `hello` as the first stdin message (seq=1).
5. Normal message exchange begins.

The CLI must not emit any `message` events until after `hello` is received and validated.

## CLI → host message kinds

### `ready`

```json
{
  "protocol": 1,
  "kind": "ready",
  "version": "0.1.0",
  "mode": "listen",
  "address": "127.0.0.1:2575"
}
```

### `message` (inbound HL7)

```json
{
  "protocol": 1,
  "kind": "message",
  "id": "msg-0001",
  "connectionId": "conn-0001",
  "direction": "inbound",
  "receivedAt": "2026-11-17T10:00:00.000Z",
  "transport": {
    "peer": "192.168.1.100:34521",
    "tls": true
  },
  "hl7": {
    "encoding": "ER7",
    "version": "2.5.1",
    "charset": "UTF-8",
    "messageType": "ADT^A01",
    "controlId": "MSG00001",
    "separators": {
      "field": "|",
      "component": "^",
      "repetition": "~",
      "escape": "\\",
      "subcomponent": "&"
    },
    "raw": "MSH|^~\\&|...",
    "segments": [
      { "name": "MSH", "fields": ["|", "^~\\&", "HIS", "CENTRAL"] },
      { "name": "PID", "fields": ["1", "", "12345", "", "Doe^John"] }
    ]
  }
}
```

**Invariants:**
- `id` is a unique CLI-generated message identifier. It is distinct from `hl7.controlId` (MSH-10).
- `hl7.raw` is authoritative. `hl7.segments` is a derived parsed view.
- For non-UTF-8 payloads, `hl7.raw` is omitted and `hl7.rawBase64` is set instead.
- Empty fields are preserved. Repetitions, components, subcomponents are preserved as raw strings.
- `hl7.charset` reflects MSH-18, or the partner-profile override if configured.

### `status`

```json
{
  "protocol": 1,
  "kind": "status",
  "connectionId": "conn-0001",
  "event": "connected",
  "peer": "192.168.1.100:34521"
}
```

Events: `connected`, `disconnected`, `error`.

### `error`

```json
{
  "protocol": 1,
  "kind": "error",
  "code": "tls_handshake_failed",
  "message": "peer certificate expired",
  "connectionId": "conn-0001"
}
```

Error codes are stable machine-readable strings. `message` is for human debugging only. No PHI in either field.

### `sent`

```json
{
  "protocol": 1,
  "kind": "sent",
  "id": "msg-0001",
  "connectionId": "conn-0001"
}
```

### `ack` (remote ACK received)

```json
{
  "protocol": 1,
  "kind": "ack",
  "id": "msg-0001",
  "connectionId": "conn-0001",
  "ackCode": "AA",
  "controlId": "MSG00001"
}
```

### `stopped`

```json
{
  "protocol": 1,
  "kind": "stopped"
}
```

## Host → CLI message kinds

### `hello`

```json
{
  "protocol": 1,
  "kind": "hello",
  "epoch": "host-instance-uuid-abc123",
  "seq": 1
}
```

Must be the first stdin message. Must have `seq: 1`.

### `ack` (acknowledge an inbound message)

```json
{
  "protocol": 1,
  "kind": "ack",
  "id": "msg-0001",
  "epoch": "host-instance-uuid-abc123",
  "seq": 2,
  "ackCode": "AA"
}
```

- `id` references the CLI `message.id` (not MSH-10).
- `ackCode` is `"AA"`, `"AE"`, or `"AR"`.
- For AE/AR, `errorCode` and `errorText` may be provided.
- The host must only send `AA` after the message is durably persisted.

### `send` (outbound message)

```json
{
  "protocol": 1,
  "kind": "send",
  "id": "out-0001",
  "epoch": "host-instance-uuid-abc123",
  "seq": 3,
  "hl7": {
    "raw": "MSH|^~\\&|..."
  }
}
```

### `ping`

```json
{
  "protocol": 1,
  "kind": "ping",
  "epoch": "host-instance-uuid-abc123",
  "seq": 4
}
```

The CLI responds with a `status` message.

### `shutdown`

```json
{
  "protocol": 1,
  "kind": "shutdown",
  "epoch": "host-instance-uuid-abc123",
  "seq": 5
}
```

The CLI begins graceful shutdown. It drains pending ACKs (up to the configured grace period), emits `stopped`, and exits with code 0.

## Deferred ACK semantics (default mode)

The default ACK mode is deferred/manual:

1. CLI receives MLLP frame and emits `message` to host.
2. Host persists the message durably.
3. Host sends `ack` with `ackCode: "AA"` only after persistence succeeds.
4. CLI constructs the HL7 ACK and sends it to the remote system.

If the host crashes before responding, the CLI must not manufacture an `AA`. The timeout outcome is "unknown delivery" — the remote system may retry. The host must implement idempotent processing.

Auto-AA mode (the CLI immediately accepts without waiting) is an explicit opt-in only.

## Error codes (stable)

| Code | Meaning |
|------|---------|
| `startup_error` | CLI failed to start |
| `not_implemented` | Feature not yet available |
| `tls_handshake_failed` | TLS negotiation failed |
| `tls_cert_expired` | Peer certificate expired |
| `port_in_use` | Bind address already in use |
| `frame_too_large` | MLLP frame exceeded maximum |
| `malformed_frame` | Invalid MLLP framing bytes |
| `hl7_parse_failed` | ER7 message could not be parsed |
| `ack_timeout` | ACK not received within timeout |
| `ack_correlation_failed` | ACK MSA-2 did not match sent control ID |
| `protocol_error` | Invalid or unexpected stdin message |
| `host_timeout` | Host did not respond within ACK deadline |
| `connection_refused` | Remote TCP connection refused |
| `connection_reset` | Remote TCP connection reset |

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Successful operation |
| 1 | Runtime failure |
| 2 | Usage or protocol error |
| 3 | Negative ACK / application rejection |
| 4 | TLS or authentication failure |
