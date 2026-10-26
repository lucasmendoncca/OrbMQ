package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/session"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server        Server         `yaml:"server"`
	Sessions      session.Limits `yaml:"sessions"`
	Observability Observability  `yaml:"observability"`
}

type Server struct {
	Address            string        `yaml:"address"`
	HandshakeTimeout   time.Duration `yaml:"handshake_timeout"`
	MaxPacketSizeBytes int           `yaml:"max_packet_size_bytes"`
}

type Observability struct {
	SummaryInterval       time.Duration `yaml:"summary_interval"`
	SaturationLogInterval time.Duration `yaml:"saturation_log_interval"`
}

func Defaults() Config {
	return Config{Server: Server{":1883", 10 * time.Second, 1 << 20}, Sessions: session.DefaultLimits(), Observability: Observability{30 * time.Second, 10 * time.Second}}
}

// Load requires one YAML mapping. Unknown fields, nulls and aliases are rejected
// rather than silently replacing or bypassing defaults.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	if len(data) > 65536 {
		return Config{}, errors.New("configuration exceeds size limit")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return Config{}, fmt.Errorf("parse configuration: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return Config{}, errors.New("configuration must be a mapping")
	}
	if err := validateNodes(&document); err != nil {
		return Config{}, err
	}
	cfg := Defaults()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("configuration must contain one document")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateNodes(node *yaml.Node) error {
	if node.Tag == "!!null" || node.Kind == yaml.AliasNode {
		return errors.New("configuration cannot contain nulls or aliases")
	}
	for _, child := range node.Content {
		if err := validateNodes(child); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) Validate() error {
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
	return nil
}
