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
	Hosts           Hosts                  `json:"hosts"`
}

type PredefinedHosts struct {
	Domain string   `json:"domain"`
	IPv4   []string `json:"ipv4"`
	IPv6   []string `json:"ipv6"`
}

type Hosts struct {
	Predefined []PredefinedHosts `json:"predefined"`
}

// UpstreamServer represents a single upstream DNS server

type UpstreamServerConfig struct {
	Address string `json:"address"`
	Type    string `json:"type"` // "udp" or "doh"
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
