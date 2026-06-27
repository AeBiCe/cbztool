package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is cb's on-disk configuration (JSON), used by `cb tag`. It lives at
// $XDG_CONFIG_HOME/cb/config.json (falling back to ~/.config/cb/config.json).
//
// Example:
//
//	{
//	  "provider": "metron",
//	  "metron":    { "username": "user", "password": "pass" },
//	  "comicvine": { "api_key": "..." }
//	}
type Config struct {
	Provider  string         `json:"provider"`
	Metron    ProviderConfig `json:"metron"`
	ComicVine ProviderConfig `json:"comicvine"`
}

// maxConfigSize caps the config file read; a credentials blob is tiny, so a
// larger file is treated as a mistake rather than slurped into memory.
const maxConfigSize = 1 << 20

// configPath returns the path to the config file, honoring XDG_CONFIG_HOME.
func configPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cb", "config.json"), nil
}

// LoadConfig reads the config file (absent is fine) and applies environment
// overrides so credentials need not sit on disk:
//
//	CB_PROVIDER, CB_METRON_USERNAME, CB_METRON_PASSWORD, CB_COMICVINE_API_KEY
func LoadConfig() (*Config, error) {
	cfg := &Config{Provider: "metron"}

	if path, err := configPath(); err == nil {
		switch info, serr := os.Stat(path); {
		case serr == nil:
			if info.Size() > maxConfigSize {
				return nil, fmt.Errorf("%s is too large (%d bytes); expected a small JSON credentials file", path, info.Size())
			}
			// The file holds API credentials; warn (don't fail) if it's exposed
			// to other users on the system.
			if info.Mode().Perm()&0o077 != 0 {
				fmt.Fprintf(os.Stderr, "cb: warning: %s is readable by other users; it contains credentials — run: chmod 600 %s\n", path, path)
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil, fmt.Errorf("read %s: %w", path, rerr)
			}
			if jerr := json.Unmarshal(data, cfg); jerr != nil {
				return nil, fmt.Errorf("parse %s: %w", path, jerr)
			}
		case !os.IsNotExist(serr):
			return nil, fmt.Errorf("stat %s: %w", path, serr)
		}
	}

	if v := os.Getenv("CB_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("CB_METRON_USERNAME"); v != "" {
		cfg.Metron.Username = v
	}
	if v := os.Getenv("CB_METRON_PASSWORD"); v != "" {
		cfg.Metron.Password = v
	}
	if v := os.Getenv("CB_COMICVINE_API_KEY"); v != "" {
		cfg.ComicVine.APIKey = v
	}
	cfg.Provider = CanonicalProvider(cfg.Provider)
	if cfg.Provider == "" {
		cfg.Provider = "metron"
	}
	return cfg, nil
}

// providerConfig returns the credentials block for the named provider (aliases
// accepted).
func (c *Config) providerConfig(name string) ProviderConfig {
	switch CanonicalProvider(name) {
	case "comicvine":
		return c.ComicVine
	default:
		return c.Metron
	}
}
