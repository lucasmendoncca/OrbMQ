package config

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/broker"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/server"
	"github.com/lucasmendoncca/OrbMQ/internal/session"
)

type Config struct {
	Security      Security              `yaml:"security"`
	Server        Server                `yaml:"server"`
	Sessions      session.Limits        `yaml:"sessions"`
	Retained      broker.RetainedLimits `yaml:"retained"`
	Observability Observability         `yaml:"observability"`
}

type Server struct {
	Address            string        `yaml:"address"`
	HandshakeTimeout   time.Duration `yaml:"handshake_timeout"`
	MaxPacketSizeBytes int           `yaml:"max_packet_size_bytes"`
	MaxConnections     int           `yaml:"max_connections"`
	MaxHandshakes      int           `yaml:"max_handshakes"`
	WriteTimeout       time.Duration `yaml:"write_timeout"`
}

type Observability struct {
	SummaryInterval       time.Duration `yaml:"summary_interval"`
	SaturationLogInterval time.Duration `yaml:"saturation_log_interval"`
}

func Defaults() Config {
	limits := server.DefaultConnectionLimits()
	return Config{
		Server:   Server{Address: "127.0.0.1:1883", HandshakeTimeout: 10 * time.Second, MaxPacketSizeBytes: 1 << 20, MaxConnections: limits.MaxConnections, MaxHandshakes: limits.MaxHandshakes, WriteTimeout: 30 * time.Second},
		Sessions: session.DefaultLimits(), Retained: broker.DefaultRetainedLimits(),
		Observability: Observability{30 * time.Second, 10 * time.Second},
	}
}

// Load requires one YAML mapping. Unknown fields, nulls and aliases are rejected
// rather than silently replacing or bypassing defaults.
func Load(path string) (Config, error) {
	cfg := Defaults()
	if err := loadYAML(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	resolve := func(value string) string {
		if value == "" || filepath.IsAbs(value) {
			return value
		}
		return filepath.Join(filepath.Dir(path), value)
	}
	cfg.Security.CredentialsFile = resolve(cfg.Security.CredentialsFile)
	if cfg.Security.TLS != nil {
		cfg.Security.TLS.CertFile = resolve(cfg.Security.TLS.CertFile)
		cfg.Security.TLS.KeyFile = resolve(cfg.Security.TLS.KeyFile)
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if err := (server.ConnectionLimits{MaxConnections: c.Server.MaxConnections, MaxHandshakes: c.Server.MaxHandshakes}).Validate(); err != nil {
		return err
	}
	if c.Server.WriteTimeout <= 0 {
		return errors.New("write timeout must be positive")
	}
	if err := c.Retained.Validate(); err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(c.Server.Address)
	if err != nil {
		return errors.New("invalid server address")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 0 || number > 65535 {
		return errors.New("invalid server port")
	}
	if c.Server.HandshakeTimeout <= 0 {
		return errors.New("handshake timeout must be positive")
	}
	if c.Server.MaxPacketSizeBytes <= 0 || c.Server.MaxPacketSizeBytes > protocol.MaxPacketSize {
		return errors.New("invalid maximum packet size")
	}
	if err := c.Sessions.Validate(); err != nil {
		return err
	}
	if c.Observability.SummaryInterval <= 0 || c.Observability.SaturationLogInterval <= 0 {
		return errors.New("observation intervals must be positive")
	}
	return c.Security.validate(c.Server.Address)
}
