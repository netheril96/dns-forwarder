package lib

import (
	"encoding/json"
	"os"
)

// Config holds the configuration for the DNS forwarder

type Config struct {
	ListenAddress   string                 `json:"listen_address"`
	ListenPort      int                    `json:"listen_port"`
	UpstreamServers []UpstreamServerConfig `json:"upstream_servers"`
	Bootstrap       string                 `json:"bootstrap"`
}

// ECSConfig holds the configuration for EDNS Client Subnet.
type ECSConfig struct {
	// Static subnet to use for ECS (e.g., "192.0.2.0/24").
	Subnet string `json:"subnet,omitempty"`

	// Dynamic IPv6 subnet from a network interface.
	Interface   string `json:"interface,omitempty"`
	PrefixV6Len int    `json:"prefix_v6_len,omitempty"`
}

// UpstreamServerConfig represents a single upstream DNS server.

type UpstreamServerConfig struct {
	Address string     `json:"address"`
	Type    string     `json:"type"` // "udp" or "doh"
	ECS     *ECSConfig `json:"ecs,omitempty"`
}

// LoadConfig reads the configuration from a JSON file

func LoadConfig(file string) (*Config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	var config Config
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}
