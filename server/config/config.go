// Package config loads the server configuration from a YAML file.
// The file path is taken from the CONFIG_PATH environment variable,
// defaulting to config.yaml next to the working directory.
package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Threathive Threathive `yaml:"threathive"`
}

type Threathive struct {
	// Enabled turns on periodic import of the Threathive blocklist.
	Enabled bool `yaml:"enabled"`

	// URL of the plain-text blocklist (one IP per line).
	URL string `yaml:"url"`

	// Interval between imports (Go duration string, e.g. 3h).
	Interval string `yaml:"interval"`

	// Source name stored with every imported report.
	Source string `yaml:"source"`

	// Report window stored with every imported report (not used directly;
    // kept for documentation / future use).
	ReportWindow string `yaml:"report_window"`
}

// Load reads the YAML config from `path` and returns it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}

	// Defaults
	if c.Threathive.URL == "" {
		c.Threathive.URL = "https://threathive.net/hiveblocklist.txt"
	}
	if c.Threathive.Interval == "" {
		c.Threathive.Interval = "3h"
	}
	if c.Threathive.Source == "" {
		c.Threathive.Source = "threathive.net"
	}
	if c.Threathive.ReportWindow == "" {
		c.Threathive.ReportWindow = "6h"
	}

	return &c, nil
}
