package main

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the reporter's configuration loaded from /etc/crowdblock-reporter/config.yaml.
// CLI flags and environment variables override values from this file.
type Config struct {
	Server   string        `yaml:"server"`
	APIKey   string        `yaml:"api_key"`
	Plugins  []string      `yaml:"plugins"`
	Threshold int          `yaml:"threshold"`
	Window   time.Duration `yaml:"window"`
	Cooldown time.Duration `yaml:"cooldown"`
	DryRun   bool          `yaml:"dry_run"`

	AuthLog  string `yaml:"auth_log"`
	Fail2Ban string `yaml:"fail2ban_log"`
}

// DefaultConfig returns a config with the same defaults as the CLI flags.
func DefaultConfig() Config {
	return Config{
		Threshold: 3,
		Window:    10 * time.Minute,
		Cooldown:  time.Hour,
		Plugins:   pluginNames(),
	}
}

// LoadConfig reads a YAML config file. Missing file is not an error.
// CLI flags and env vars override loaded values afterwards in main().
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
