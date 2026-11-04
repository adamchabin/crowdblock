// Package config loads the server configuration from a YAML file.
// The file path is taken from the CONFIG_PATH environment variable,
// defaulting to config.yaml next to the working directory.
package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ExternalLists []ExternalList `yaml:"external_lists"`
}

// ExternalList describes one external blocklist to import periodically.
type ExternalList struct {
	// Enabled turns on periodic import of this list.
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

	// Format of the list file. Currently only "plain" (one IP per line,
	// lines starting with # are comments) is supported.
	Format string `yaml:"format"`

	// Country, when set, forces this ISO 3166-1 alpha-2 code for every IP
	// imported from this list in statistics and on the map, overriding the
	// GeoIP lookup. Useful when the external list is country-specific but
	// the GeoIP database doesn't resolve those addresses correctly.
	Country string `yaml:"country"`
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

	// External lists defaults.
	for i := range c.ExternalLists {
		l := &c.ExternalLists[i]
		if l.URL == "" {
			l.URL = "https://threathive.net/hiveblocklist.txt"
		}
		if l.Interval == "" {
			l.Interval = "3h"
		}
		if l.Source == "" {
			l.Source = "external"
		}
		if l.ReportWindow == "" {
			l.ReportWindow = "6h"
		}
		if l.Format == "" {
			l.Format = "plain"
		}
	}

	return &c, nil
}
