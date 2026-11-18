# ADR-002: Configuration file format

**Status:** Accepted  
**Date:** 2026-07-xx  
**Milestone:** M5

## Context

The CLI needs a human-editable configuration file for deployment settings that are too verbose for command-line flags: TLS paths, ACK timeouts, connection limits, partner profiles. The plan explicitly says to choose one format.

Candidates: TOML, JSON, YAML.

## Decision

**TOML** is selected.

Reasons:
1. TOML is designed for human-maintained configuration. Comments, section headers, and clear scalar types make operator-managed files readable without tooling.
2. JSON has no comment support, which matters for deployment configuration where operators annotate intent alongside values.
3. YAML's indentation-sensitivity and Norway-bool ambiguity have caused production incidents in similar tools. TOML avoids both.
4. The `miyako/dashboard` project already uses TOML for `procs.toml` resource files, establishing precedent in the ecosystem.
5. `github.com/BurntSushi/toml` is a stable, widely-used Go library with no transitive dependencies.

## Consequences

- Configuration files use `.toml` extension.
- The example file is `mllp.example.toml`.
- CLI flags always override config file values (flags have higher precedence).
- Partner-specific profiles will be separate TOML files, loaded alongside the base config.
- The config package (`internal/config`) owns loading, validation, and default-setting.
- JSON and YAML config formats are not supported.
