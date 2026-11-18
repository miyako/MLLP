# ADR-001: Implementation language

**Status:** Accepted  
**Date:** 2026-07-xx  
**Milestone:** M0

## Context

The project requires a portable, self-contained native CLI that runs on macOS (Apple Silicon + Intel) and Windows x64 without any runtime installation. The implementation team evaluated Go and Rust as candidates.

Both toolchains are present on the development machine:
- Go 1.25.1 (darwin/arm64)
- Rust 1.91.0

## Evaluation criteria

| Criterion | Go | Rust |
|---|---|---|
| TCP networking | Standard library (`net`) | `tokio` + `async-std` (external) |
| TLS / mTLS | Standard library (`crypto/tls`) | `rustls` or `native-tls` (external) |
| Memory safety | Managed (GC) | Compile-time guarantees |
| Cross-compilation | `GOOS`/`GOARCH` env vars | `cargo` + target toolchains |
| 4D CLI precedent | Yes (`miyako/dashboard`) | None |
| HL7 ecosystem | Established Go libraries | Developing |
| Implementation velocity | Fast | Slower due to ownership/borrow |
| Static binary distribution | Excellent | Excellent |
| Binary size | ~5–10 MB | ~2–5 MB |

## Decision

**Go** is selected for the production implementation.

Rationale:

1. The existing `miyako/dashboard` CLI establishes Go as the established CLI language for this ecosystem. The build scripts, CI workflows, signing/notarization procedures, and 4D component distribution machinery are all Go-oriented.

2. The project's risk profile centres on correctness, interoperability, ACK semantics, and operational reliability — not raw computational performance. Go's GC-managed memory model is appropriate for a network bridge.

3. Standard-library TCP, TLS, and JSON in Go meet all project requirements without external dependencies. This keeps the dependency surface minimal and reproducible.

4. Go cross-compilation (`GOOS=windows GOARCH=amd64`) is a single command with no additional toolchain configuration.

5. Implementation velocity matters: the M0 spike confirmed that the MLLP framer, HL7 parser, and protocol codec can all be written and tested within the first milestone window.

## Rust evaluation result

A minimal Rust prototype was considered but not fully implemented in M0. The Go spike produced working, tested implementations of all M0 deliverables within the milestone window. The Rust evaluation is recorded as considered but not pursued for v1.

If a future requirement arises for embedding the codec as a C-compatible shared library, or if the project requires WASM targets, Rust's advantages should be re-evaluated.

## Consequences

- All source code under `internal/` and `cmd/` is Go.
- The Go module is `github.com/miyako/mllp`.
- The minimum supported Go version is 1.23 (matches the `go.mod` constraint).
- No CGO. All builds are `CGO_ENABLED=0` for portability.
- The `miyako/dashboard` CI patterns (build matrix, Python scripts, tool4d acceptance) can be reused for the release pipeline.
