// Package config loads and validates MLLP configuration from a TOML file.
//
// Configuration values set via CLI flags always override config file values.
// Partner-specific profiles override only their explicitly declared settings.
package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is the root configuration structure.
type Config struct {
	Listener ListenerConfig `toml:"listener"`
	Connector ConnectorConfig `toml:"connector"`
	TLS      TLSConfig      `toml:"tls"`
	ACK      ACKConfig      `toml:"ack"`
	Host     HostConfig     `toml:"host"`
}

// ListenerConfig holds inbound listener settings.
type ListenerConfig struct {
	Bind           string `toml:"bind"`
	MaxConnections int    `toml:"max_connections"`
	MaxFrameBytes  int    `toml:"max_frame_bytes"`
}

// ConnectorConfig holds outbound connection settings.
type ConnectorConfig struct {
	Host           string `toml:"host"`
	Port           string `toml:"port"`
	MaxFrameBytes  int    `toml:"max_frame_bytes"`
	DialTimeoutSec int    `toml:"dial_timeout_sec"`
}

// TLSConfig holds TLS settings shared between listener and connector.
type TLSConfig struct {
	Enabled    bool   `toml:"enabled"`
	CertFile   string `toml:"cert"`
	KeyFile    string `toml:"key"`
	CAFile     string `toml:"ca"`
	VerifyPeer bool   `toml:"verify_peer"`
	MinVersion string `toml:"min_version"` // "1.2" or "1.3"
}

// ACKConfig holds acknowledgement mode settings.
type ACKConfig struct {
	Mode      string `toml:"mode"`       // "manual" (default), "auto-aa"
	TimeoutMs int    `toml:"timeout_ms"` // 0 = 30000
}

// HostConfig holds host protocol settings.
type HostConfig struct {
	Protocol           int `toml:"protocol"`            // must be 1
	MaxPendingMessages int `toml:"max_pending_messages"` // 0 = 100
}

// Load reads a TOML config file from path and returns the parsed Config.
// It does not apply defaults; callers should apply defaults after loading.
func Load(path string) (*Config, error) {
	if path == "" {
		return &Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var cfg Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: validate %s: %w", path, err)
	}
	return &cfg, nil
}

// Validate checks the loaded configuration for semantic errors.
func (c *Config) Validate() error {
	if c.Host.Protocol != 0 && c.Host.Protocol != 1 {
		return fmt.Errorf("host.protocol must be 1, got %d", c.Host.Protocol)
	}
	if c.ACK.Mode != "" && c.ACK.Mode != "manual" && c.ACK.Mode != "auto-aa" {
		return fmt.Errorf("ack.mode must be \"manual\" or \"auto-aa\", got %q", c.ACK.Mode)
	}
	if c.TLS.MinVersion != "" && c.TLS.MinVersion != "1.2" && c.TLS.MinVersion != "1.3" {
		return fmt.Errorf("tls.min_version must be \"1.2\" or \"1.3\", got %q", c.TLS.MinVersion)
	}
	if c.TLS.CertFile != "" && c.TLS.KeyFile == "" {
		return errors.New("tls.cert requires tls.key")
	}
	if c.TLS.KeyFile != "" && c.TLS.CertFile == "" {
		return errors.New("tls.key requires tls.cert")
	}
	return nil
}

// ApplyDefaults fills in zero values with their production defaults.
func (c *Config) ApplyDefaults() {
	if c.Listener.Bind == "" {
		c.Listener.Bind = "127.0.0.1:2575"
	}
	if c.Listener.MaxConnections == 0 {
		c.Listener.MaxConnections = 100
	}
	if c.Listener.MaxFrameBytes == 0 {
		c.Listener.MaxFrameBytes = 1 << 20 // 1 MiB
	}
	if c.Connector.Port == "" {
		c.Connector.Port = "2575"
	}
	if c.ACK.Mode == "" {
		c.ACK.Mode = "manual"
	}
	if c.ACK.TimeoutMs == 0 {
		c.ACK.TimeoutMs = 30000
	}
	if c.Host.Protocol == 0 {
		c.Host.Protocol = 1
	}
	if c.Host.MaxPendingMessages == 0 {
		c.Host.MaxPendingMessages = 100
	}
	if c.TLS.MinVersion == "" {
		c.TLS.MinVersion = "1.2"
	}
}
