// Package config loads cbz's settings. Layers apply in order, each overriding
// only the keys it sets:
//
//  1. built-in defaults (Default)
//  2. $XDG_CONFIG_HOME/cbz/config.json (falling back to ~/.config/cbz), if present
//  3. an explicit file from -c/--config or CBZ_CONFIG, which must exist
//  4. environment variables (CBZ_PROVIDER and the credential variables)
//
// The file holds settings only — no secrets — so it is safe to keep in a
// dotfiles repo or generate with home-manager (which puts it in the
// world-readable Nix store). Credentials come from environment variables or
// from files named by the *_file keys (e.g. an agenix/sops secret).
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"codeberg.org/Nikode/cbztool/internal/comicinfo"
	"codeberg.org/Nikode/cbztool/internal/term"
)

// Config is cbz's resolved configuration.
type Config struct {
	Providers   Providers              `json:"providers"`
	Rename      Rename                 `json:"rename"`
	Credentials map[string]Credentials `json:"credentials,omitempty"`

	files       []string // config files applied, lowest precedence first
	envProvider string   // CBZ_PROVIDER, if set
}

// Providers controls the online metadata sources used by `cbz tag`.
type Providers struct {
	// Order is the precedence of providers: the first is searched first and
	// wins single-value fields when sources disagree. Providers not listed
	// follow in name order.
	Order []string `json:"order"`
	// Prefer overrides Order for individual ComicInfo fields, e.g.
	// {"Summary": "comicvine"}. List fields that are merged from every source
	// (Genre, Tags, …) can't be preferred; Year stands for the whole date.
	Prefer map[string]string `json:"prefer,omitempty"`
	// CacheTTL is how long a cached API response stays fresh.
	CacheTTL Duration `json:"cache_ttl"`
}

// Rename controls `cbz rename`.
type Rename struct {
	// Template builds a filename from ComicInfo fields; see `cbz rename -h`.
	Template string `json:"template"`
}

// Credentials locates one provider's secrets. Username is not secret and may
// be inline; passwords and API keys are read from files (or the environment).
type Credentials struct {
	Username     string `json:"username,omitempty"`
	PasswordFile string `json:"password_file,omitempty"`
	APIKeyFile   string `json:"api_key_file,omitempty"`

	// Inline secrets are rejected by Load; these exist only so the error can
	// explain how to migrate instead of reporting an unknown key.
	InlinePassword string `json:"password,omitempty"`
	InlineAPIKey   string `json:"api_key,omitempty"`

	// Password and APIKey hold the resolved secrets; never serialized.
	Password string `json:"-"`
	APIKey   string `json:"-"`
}

// Duration is a time.Duration written as a string ("24h", "90m") in JSON.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"24h\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	if v <= 0 {
		return fmt.Errorf("duration %q must be positive", s)
	}
	d.Duration = v
	return nil
}

// MarshalJSON writes the duration the way people write it: "24h", not "24h0m0s".
func (d Duration) MarshalJSON() ([]byte, error) {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return json.Marshal(s)
}

// DefaultTemplate is the built-in `cbz rename` template.
const DefaultTemplate = "{Series}[ ({Year})] #{Number:3}"

// Default returns the built-in settings that every config file layers onto.
func Default() *Config {
	return &Config{
		Providers: Providers{
			Order:    []string{"metron", "comicvine"},
			CacheTTL: Duration{24 * time.Hour},
		},
		Rename: Rename{Template: DefaultTemplate},
	}
}

// maxFileSize caps a config or secret file read; both are tiny, so anything
// larger is a mistake rather than something to slurp into memory.
const maxFileSize = 1 << 20

// DefaultPath is the config file read when present:
// $XDG_CONFIG_HOME/cbz/config.json, falling back to ~/.config/cbz/config.json.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cbz", "config.json"), nil
}

// Load builds the configuration from every layer. explicit is the -c/--config
// path ("" for none, in which case CBZ_CONFIG is consulted). Provider names are
// normalized with canon (which resolves aliases) and must be one of known.
// Secret files are not read here — only commands that talk to a provider need
// them, and they call ResolveSecrets — so a missing API key file never breaks
// an offline command.
func Load(explicit string, known []string, canon func(string) string) (*Config, error) {
	cfg := Default()
	if p, err := DefaultPath(); err == nil {
		if err := cfg.apply(p, false); err != nil {
			return nil, err
		}
	}
	if explicit == "" {
		explicit = os.Getenv("CBZ_CONFIG")
	}
	if explicit != "" {
		if err := cfg.apply(explicit, true); err != nil {
			return nil, err
		}
	}
	cfg.applyEnv()
	if err := cfg.validate(known, canon); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Files lists the config files that were applied, lowest precedence first.
func (c *Config) Files() []string { return c.files }

// apply decodes the file at path on top of c. Keys the file leaves out keep
// their current value; a credentials entry is replaced as a whole. A missing
// file is an error only when required.
func (c *Config) apply(path string, required bool) error {
	path, err := expandHome(path)
	if err != nil {
		return err
	}
	data, err := readSmall(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return nil
		}
		return err
	}
	// The pre-1.0 layout had top-level provider/metron/comicvine keys and inline
	// secrets; name the migration rather than report an unknown field.
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) == nil {
		for _, old := range []string{"provider", "metron", "comicvine"} {
			if _, ok := top[old]; ok {
				return fmt.Errorf("%s uses the old config layout (top-level %q); move settings under "+
					"\"providers\" and secrets into files referenced from \"credentials\" — "+
					"see `cbz config init` or config.example.json", path, old)
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // a typo should fail loudly, not be ignored
	if err := dec.Decode(c); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for name, cr := range c.Credentials {
		if cr.InlinePassword != "" || cr.InlineAPIKey != "" {
			return fmt.Errorf("%s: credentials.%s holds a secret inline; move it to a file "+
				"(chmod 600) and reference it with password_file / api_key_file, or use an "+
				"environment variable — the config file is meant to be shareable", path, name)
		}
	}
	c.files = append(c.files, path)
	return nil
}

// envCredentials maps environment variables to the credential they set.
// They take precedence over the *_file keys.
var envCredentials = []struct {
	env, provider string
	set           func(*Credentials, string)
}{
	{"CBZ_METRON_USERNAME", "metron", func(c *Credentials, v string) { c.Username = v }},
	{"CBZ_METRON_PASSWORD", "metron", func(c *Credentials, v string) { c.Password = v }},
	{"CBZ_COMICVINE_API_KEY", "comicvine", func(c *Credentials, v string) { c.APIKey = v }},
}

// applyEnv applies the environment layer: CBZ_PROVIDER moves a provider to the
// front of the order, and the credential variables fill in secrets.
func (c *Config) applyEnv() {
	if v := os.Getenv("CBZ_PROVIDER"); v != "" {
		c.envProvider = v // moved to the front once names are canonical (validate)
	}
	for _, e := range envCredentials {
		if v := os.Getenv(e.env); v != "" {
			if c.Credentials == nil {
				c.Credentials = map[string]Credentials{}
			}
			cr := c.Credentials[e.provider]
			e.set(&cr, v)
			c.Credentials[e.provider] = cr
		}
	}
}

// validate normalizes provider names (canon) and checks every setting.
func (c *Config) validate(known []string, canon func(string) string) error {
	checkName := func(where, name string) (string, error) {
		n := canon(name)
		if !slices.Contains(known, n) {
			return "", fmt.Errorf("config: %s: unknown provider %q (known: %s)", where, name, strings.Join(known, ", "))
		}
		return n, nil
	}

	order := make([]string, 0, len(c.Providers.Order))
	for _, name := range c.Providers.Order {
		n, err := checkName("providers.order", name)
		if err != nil {
			return err
		}
		if slices.Contains(order, n) {
			return fmt.Errorf("config: providers.order lists %q twice", n)
		}
		order = append(order, n)
	}
	if c.envProvider != "" {
		n, err := checkName("CBZ_PROVIDER", c.envProvider)
		if err != nil {
			return err
		}
		order = append([]string{n}, slices.DeleteFunc(order, func(s string) bool { return s == n })...)
	}
	c.Providers.Order = order

	prefer := make(map[string]string, len(c.Providers.Prefer))
	for field, prov := range c.Providers.Prefer {
		f, ok := comicinfo.CanonicalField(field)
		switch {
		case !ok:
			return fmt.Errorf("config: providers.prefer: unknown ComicInfo field %q", field)
		case comicinfo.IsUnionField(f):
			return fmt.Errorf("config: providers.prefer: %s is merged from every provider, so it can't be preferred", f)
		case f == "Month" || f == "Day":
			return fmt.Errorf("config: providers.prefer: use Year — the date (Year/Month/Day) is taken from one provider as a unit")
		}
		n, err := checkName("providers.prefer."+f, prov)
		if err != nil {
			return err
		}
		prefer[f] = n
	}
	c.Providers.Prefer = prefer

	creds := make(map[string]Credentials, len(c.Credentials))
	for name, cr := range c.Credentials {
		n, err := checkName("credentials", name)
		if err != nil {
			return err
		}
		if _, dup := creds[n]; dup {
			return fmt.Errorf("config: credentials for %s are given twice (under an alias)", n)
		}
		creds[n] = cr
	}
	c.Credentials = creds

	if strings.TrimSpace(c.Rename.Template) == "" {
		return fmt.Errorf("config: rename.template is empty")
	}
	return nil
}

// ResolveSecrets reads each password_file / api_key_file whose secret isn't
// already set from the environment, so a missing or unreadable secret file is
// reported before any provider is contacted.
func (c *Config) ResolveSecrets() error {
	for name, cr := range c.Credentials {
		var err error
		if cr.Password == "" && cr.PasswordFile != "" {
			if cr.Password, err = readSecret(cr.PasswordFile); err != nil {
				return fmt.Errorf("credentials.%s.password_file: %w", name, err)
			}
		}
		if cr.APIKey == "" && cr.APIKeyFile != "" {
			if cr.APIKey, err = readSecret(cr.APIKeyFile); err != nil {
				return fmt.Errorf("credentials.%s.api_key_file: %w", name, err)
			}
		}
		c.Credentials[name] = cr
	}
	return nil
}

// readSecret returns the trimmed contents of a secret file. Paths must be
// absolute or start with "~/": a relative path would depend on the directory
// cbz happens to run from. A file other users can read gets a warning.
func readSecret(path string) (string, error) {
	path, err := expandHome(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%q must be an absolute path (or start with ~/)", path)
	}
	data, err := readSmall(path)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
		term.Warnf("%s is readable by other users; it holds a secret — run: chmod 600 %s", path, path)
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return s, nil
}

// readSmall reads a file of at most maxFileSize bytes.
func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxFileSize {
		return nil, fmt.Errorf("%s is too large (%d bytes)", path, info.Size())
	}
	return io.ReadAll(io.LimitReader(f, maxFileSize))
}

// expandHome replaces a leading "~/" with the user's home directory.
func expandHome(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~/")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}
