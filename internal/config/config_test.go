package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var known = []string{"comicvine", "metron"}

// canon mirrors provider.CanonicalProvider (aliases mt/cv).
func canon(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "mt":
		return "metron"
	case "cv":
		return "comicvine"
	}
	return s
}

// isolate points the default config location at an empty temp dir and clears
// every CB_* variable, so a test sees only the files and env it sets up.
func isolate(t *testing.T) (cfgDir string) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	for _, v := range []string{"CBZ_CONFIG", "CBZ_PROVIDER", "CBZ_METRON_TOKEN", "CBZ_COMICVINE_API_KEY"} {
		t.Setenv(v, "")
	}
	dir := filepath.Join(xdg, "cbztool")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, path, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaults(t *testing.T) {
	isolate(t)
	cfg, err := loadAll("", known, canon)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Providers.Order, []string{"metron", "comicvine"}) ||
		cfg.Providers.CacheTTL.Duration != 24*time.Hour ||
		cfg.Rename.Template != DefaultTemplate || len(cfg.Files()) != 0 {
		t.Errorf("defaults = %+v", cfg)
	}
}

func TestLayering(t *testing.T) {
	dir := isolate(t)
	secret := write(t, filepath.Join(t.TempDir(), "pw"), "hunter2\n", 0o600)
	write(t, filepath.Join(dir, "config.json"), `{
		"providers": {"order": ["cv"], "cache_ttl": "2h", "prefer": {"summary": "cv"}},
		"credentials": {"metron": {"api_key_file": "`+secret+`"}}
	}`, 0o644)
	extra := write(t, filepath.Join(t.TempDir(), "extra.json"), `{"rename": {"template": "{Series} {Number}"}}`, 0o644)

	cfg, err := loadAll(extra, known, canon)
	if err != nil {
		t.Fatal(err)
	}
	// Default file's keys survive the -c layer, which only set the template.
	if !slices.Equal(cfg.Providers.Order, []string{"comicvine"}) {
		t.Errorf("order = %v", cfg.Providers.Order)
	}
	if cfg.Providers.CacheTTL.Duration != 2*time.Hour {
		t.Errorf("cache_ttl = %v", cfg.Providers.CacheTTL)
	}
	if cfg.Providers.Prefer["Summary"] != "comicvine" {
		t.Errorf("prefer = %v (want canonical field and provider)", cfg.Providers.Prefer)
	}
	if cfg.Rename.Template != "{Series} {Number}" {
		t.Errorf("template = %q", cfg.Rename.Template)
	}
	if m := cfg.Credentials["metron"]; m.APIKey != "hunter2" {
		t.Errorf("metron credentials = %+v", m)
	}
	if len(cfg.Files()) != 2 {
		t.Errorf("files = %v", cfg.Files())
	}
}

func TestEnv(t *testing.T) {
	dir := isolate(t)
	// The token file doesn't exist: the env var supplies the secret, so the
	// file is never read.
	write(t, filepath.Join(dir, "config.json"), `{"credentials": {"metron": {"api_key_file": "/nonexistent/pw"}}}`, 0o644)
	t.Setenv("CBZ_METRON_TOKEN", "env-tok")
	t.Setenv("CBZ_COMICVINE_API_KEY", "env-key")
	t.Setenv("CBZ_PROVIDER", "cv")

	cfg, err := loadAll("", known, canon)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Providers.Order, []string{"comicvine", "metron"}) {
		t.Errorf("order = %v", cfg.Providers.Order)
	}
	if m := cfg.Credentials["metron"]; m.APIKey != "env-tok" {
		t.Errorf("metron = %+v", m)
	}
	if cfg.Credentials["comicvine"].APIKey != "env-key" {
		t.Errorf("comicvine = %+v", cfg.Credentials["comicvine"])
	}
}

func TestCBConfigEnv(t *testing.T) {
	isolate(t)
	p := write(t, filepath.Join(t.TempDir(), "c.json"), `{"rename": {"template": "{Title}"}}`, 0o644)
	t.Setenv("CBZ_CONFIG", p)
	cfg, err := loadAll("", known, canon)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Rename.Template != "{Title}" {
		t.Errorf("template = %q", cfg.Rename.Template)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct{ name, file, want string }{
		{"unknown key", `{"renmae": {}}`, "unknown field"},
		{"old layout", `{"provider": "metron", "metron": {"username": "u", "password": "p"}}`, "old config layout"},
		{"inline password", `{"credentials": {"metron": {"username": "u", "password": "p"}}}`, "API Tokens"},
		{"metron password file", `{"credentials": {"metron": {"username": "u", "password_file": "/pw"}}}`, "api_key_file"},
		{"inline api key", `{"credentials": {"comicvine": {"api_key": "k"}}}`, "api_key_file"},
		{"unknown provider", `{"providers": {"order": ["gcd"]}}`, "unknown provider"},
		{"duplicate order", `{"providers": {"order": ["metron", "mt"]}}`, "twice"},
		{"alias duplicate creds", `{"credentials": {"metron": {}, "mt": {}}}`, "twice"},
		{"union field preferred", `{"providers": {"prefer": {"Genre": "metron"}}}`, "merged"},
		{"month preferred", `{"providers": {"prefer": {"Month": "metron"}}}`, "use Year"},
		{"unknown field preferred", `{"providers": {"prefer": {"Nope": "metron"}}}`, "unknown ComicInfo field"},
		{"bad ttl", `{"providers": {"cache_ttl": "-1h"}}`, "positive"},
		{"empty template", `{"rename": {"template": " "}}`, "template"},
		{"relative secret", `{"credentials": {"metron": {"api_key_file": "pw"}}}`, "absolute"},
		{"missing secret", `{"credentials": {"metron": {"api_key_file": "/nonexistent/pw"}}}`, "api_key_file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := isolate(t)
			write(t, filepath.Join(dir, "config.json"), c.file, 0o644)
			_, err := loadAll("", known, canon)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want containing %q", err, c.want)
			}
		})
	}
}

func TestExplicitMissing(t *testing.T) {
	isolate(t)
	if _, err := loadAll(filepath.Join(t.TempDir(), "nope.json"), known, canon); err == nil {
		t.Error("a missing -c file should be an error")
	}
}

func TestEmptySecret(t *testing.T) {
	dir := isolate(t)
	secret := write(t, filepath.Join(t.TempDir(), "pw"), " \n", 0o600)
	write(t, filepath.Join(dir, "config.json"), `{"credentials": {"metron": {"api_key_file": "`+secret+`"}}}`, 0o644)
	if _, err := loadAll("", known, canon); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %v, want an empty-secret error", err)
	}
}

// TestExampleMatchesRepoFile keeps config.example.json and `cbz config init`
// output identical, and proves the example loads (its secret files aside).
func TestExampleMatchesRepoFile(t *testing.T) {
	data, err := os.ReadFile("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != Example {
		t.Error("config.example.json differs from config.Example; keep them identical")
	}

	dir := isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	secrets := filepath.Join(home, ".config", "cbztool")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(secrets, "metron-token"), "tok", 0o600)
	write(t, filepath.Join(secrets, "comicvine-api-key"), "key", 0o600)
	write(t, filepath.Join(dir, "config.json"), Example, 0o644)
	cfg, err := loadAll("", known, canon)
	if err != nil {
		t.Fatal(err)
	}
	def := Default()
	if !slices.Equal(cfg.Providers.Order, def.Providers.Order) || cfg.Providers.CacheTTL != def.Providers.CacheTTL || cfg.Rename != def.Rename {
		t.Errorf("example differs from the built-in defaults: %+v", cfg)
	}
	if cfg.Credentials["metron"].APIKey != "tok" || cfg.Credentials["comicvine"].APIKey != "key" {
		t.Errorf("example secrets not resolved: %+v", cfg.Credentials)
	}
}

// loadAll is Load followed by ResolveSecrets, as the tag path does.
func loadAll(explicit string, known []string, canon func(string) string) (*Config, error) {
	cfg, err := Load(explicit, known, canon)
	if err != nil {
		return nil, err
	}
	return cfg, cfg.ResolveSecrets()
}

// TestLoadSkipsSecrets: a broken secret file doesn't stop an offline command
// from loading its settings.
func TestLoadSkipsSecrets(t *testing.T) {
	dir := isolate(t)
	write(t, filepath.Join(dir, "config.json"), `{"credentials": {"metron": {"api_key_file": "/nonexistent/pw"}}}`, 0o644)
	cfg, err := Load("", known, canon)
	if err != nil {
		t.Fatalf("Load read a secret file: %v", err)
	}
	if err := cfg.ResolveSecrets(); err == nil {
		t.Error("ResolveSecrets should report the missing file")
	}
}
