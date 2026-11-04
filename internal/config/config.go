package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Accounts []Account `json:"accounts"`
	Defaults Defaults  `json:"defaults"`
}

type Account struct {
	ID        string          `json:"id"`
	Label     string          `json:"label"`
	Email     string          `json:"email"`
	IMAP      IMAPConfig      `json:"imap"`
	SMTP      SMTPConfig      `json:"smtp"`
	Auth      AuthConfig      `json:"auth"`
	Signature SignatureConfig `json:"signature"`
}

// SignatureConfig points to the account's email signature. File is a path to a
// signature file (an Apple Mail ".mailsignature", or a ".html"/".txt" file),
// absolute or relative to the mailbridge config directory. Signatures imported
// from Apple Mail at setup are copied under "signatures/" and referenced here,
// so the running server stays self-contained and never reads ~/Library.
// An empty config means "no signature".
type SignatureConfig struct {
	File string `json:"file,omitempty"`
}

type IMAPConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	TLS  bool   `json:"tls"`
}

func (c IMAPConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	StartTLS bool   `json:"starttls"`
	TLS      bool   `json:"tls"`
}

func (c SMTPConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

type AuthConfig struct {
	Type            string `json:"type"`
	KeychainService string `json:"keychain_service"`
	KeychainAccount string `json:"keychain_account"`
	EnvVariable     string `json:"env_variable"`
}

type Defaults struct {
	MaxFetch       int `json:"max_fetch"`
	BodyMaxChars   int `json:"body_max_chars"`
	TimeoutSeconds int `json:"timeout_seconds"`
}

func (c *Config) Account(id string) *Account {
	for i := range c.Accounts {
		if c.Accounts[i].ID == id {
			return &c.Accounts[i]
		}
	}
	return nil
}

func Load() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	path := filepath.Join(home, ".config", "mailbridge", "accounts.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config JSON: %w", err)
	}

	if cfg.Defaults.MaxFetch == 0 {
		cfg.Defaults.MaxFetch = 50
	}
	if cfg.Defaults.BodyMaxChars == 0 {
		cfg.Defaults.BodyMaxChars = 10000
	}
	if cfg.Defaults.TimeoutSeconds == 0 {
		cfg.Defaults.TimeoutSeconds = 30
	}

	if len(cfg.Accounts) == 0 {
		return nil, fmt.Errorf("no accounts configured in %s", path)
	}

	for i, acc := range cfg.Accounts {
		if acc.ID == "" {
			return nil, fmt.Errorf("account %d: missing id", i)
		}
		if acc.Email == "" {
			return nil, fmt.Errorf("account %q: missing email", acc.ID)
		}
	}

	return &cfg, nil
}
