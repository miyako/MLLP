package config

import (
	"os"
	"testing"
)

func TestLoadEmpty(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyDefaults()
	if cfg.Listener.Bind != "127.0.0.1:2575" {
		t.Errorf("default bind: got %q", cfg.Listener.Bind)
	}
	if cfg.ACK.Mode != "manual" {
		t.Errorf("default ack mode: got %q", cfg.ACK.Mode)
	}
}

const validTOML = `
[listener]
bind = "127.0.0.1:2575"
max_connections = 50
max_frame_bytes = 524288

[tls]
enabled = true
cert = "server.crt"
key = "server.key"
ca = "ca.crt"
verify_peer = true
min_version = "1.2"

[ack]
mode = "manual"
timeout_ms = 30000

[host]
protocol = 1
max_pending_messages = 100
`

func TestLoadValidTOML(t *testing.T) {
	f, err := os.CreateTemp("", "mllp-config-*.toml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString(validTOML)
	f.Close()

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listener.Bind != "127.0.0.1:2575" {
		t.Errorf("bind: got %q", cfg.Listener.Bind)
	}
	if cfg.Listener.MaxConnections != 50 {
		t.Errorf("max_connections: got %d", cfg.Listener.MaxConnections)
	}
	if !cfg.TLS.Enabled {
		t.Error("tls.enabled should be true")
	}
	if cfg.TLS.CertFile != "server.crt" {
		t.Errorf("tls.cert: got %q", cfg.TLS.CertFile)
	}
	if cfg.ACK.Mode != "manual" {
		t.Errorf("ack.mode: got %q", cfg.ACK.Mode)
	}
	if cfg.Host.Protocol != 1 {
		t.Errorf("host.protocol: got %d", cfg.Host.Protocol)
	}
}

func TestLoadInvalidTOML(t *testing.T) {
	f, err := os.CreateTemp("", "mllp-bad-*.toml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString("not = valid [ toml")
	f.Close()
	_, err = Load(f.Name())
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
}

func TestValidateInvalidProtocol(t *testing.T) {
	cfg := &Config{Host: HostConfig{Protocol: 2}}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for protocol=2")
	}
}

func TestValidateInvalidACKMode(t *testing.T) {
	cfg := &Config{ACK: ACKConfig{Mode: "instant"}}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid ack mode")
	}
}

func TestValidateTLSCertWithoutKey(t *testing.T) {
	cfg := &Config{TLS: TLSConfig{CertFile: "cert.pem"}}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for cert without key")
	}
}

func TestValidateInvalidTLSVersion(t *testing.T) {
	cfg := &Config{TLS: TLSConfig{MinVersion: "1.0"}}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for TLS 1.0")
	}
}

func TestApplyDefaultsIdempotent(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.ApplyDefaults() // second call should not change values
	if cfg.Listener.Bind != "127.0.0.1:2575" {
		t.Errorf("double ApplyDefaults changed bind: %q", cfg.Listener.Bind)
	}
}
