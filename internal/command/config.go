package command

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"codeberg.org/Nikode/cbztool/internal/config"
	"codeberg.org/Nikode/cbztool/internal/provider"
)

// configFile is the -c/--config path given before the command ("" for none).
var configFile string

// SetConfigFile records the -c/--config path that loadConfig layers over the
// default config. The file must exist: unlike the default location, an
// explicitly named config that's missing is a mistake worth reporting.
func SetConfigFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	configFile = path
	return nil
}

// loadConfig loads cbz's configuration (see package config), validating
// provider names against the registered backends.
func loadConfig() (*config.Config, error) {
	return config.Load(configFile, provider.Names(), provider.CanonicalProvider)
}

// Config implements `cbz config <path|show|init>`: inspect or create the
// configuration.
func Config(args []string) error {
	usage := "usage: cbz config path | show | init [--force] [file]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "path":
		return configPath()
	case "show":
		return configShow()
	case "init":
		return configInit(args[1:])
	default:
		return fmt.Errorf("config: unknown subcommand %q\n%s", args[0], usage)
	}
}

// configPath prints the files cbz reads, in precedence order, and whether each
// exists.
func configPath() error {
	def, err := config.DefaultPath()
	if err != nil {
		return err
	}
	paths := []string{def}
	explicit := configFile
	if explicit == "" {
		explicit = os.Getenv("CBZ_CONFIG")
	}
	if explicit != "" {
		paths = append(paths, explicit)
	}
	for _, p := range paths {
		state := "missing"
		if _, err := os.Stat(p); err == nil {
			state = "found"
		}
		fmt.Printf("%s (%s)\n", Sanitize(p), state)
	}
	return nil
}

// configShow prints the effective configuration (every layer applied) as
// JSON. Secrets are never printed: a credential shows only where it came from.
func configShow() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.ResolveSecrets(); err != nil {
		return err
	}
	type shownCreds struct {
		Username     string `json:"username,omitempty"`
		PasswordFile string `json:"password_file,omitempty"`
		APIKeyFile   string `json:"api_key_file,omitempty"`
		Password     string `json:"password,omitempty"`
		APIKey       string `json:"api_key,omitempty"`
	}
	shown := struct {
		Providers   config.Providers      `json:"providers"`
		Rename      config.Rename         `json:"rename"`
		Credentials map[string]shownCreds `json:"credentials,omitempty"`
	}{Providers: cfg.Providers, Rename: cfg.Rename, Credentials: map[string]shownCreds{}}
	for name, c := range cfg.Credentials {
		s := shownCreds{Username: c.Username, PasswordFile: c.PasswordFile, APIKeyFile: c.APIKeyFile}
		if c.Password != "" {
			s.Password = "(set)"
		}
		if c.APIKey != "" {
			s.APIKey = "(set)"
		}
		shown.Credentials[name] = s
	}
	out, err := json.MarshalIndent(shown, "", "  ")
	if err != nil {
		return err
	}
	if files := cfg.Files(); len(files) > 0 {
		fmt.Fprintln(os.Stderr, "# from:", Sanitize(fmt.Sprint(files)))
	} else {
		fmt.Fprintln(os.Stderr, "# from: built-in defaults (no config file)")
	}
	fmt.Println(string(out))
	return nil
}

// configInit writes a starter config (config.Example) to file, or to the
// default location. It refuses to overwrite unless --force is set.
func configInit(args []string) error {
	fs := flag.NewFlagSet("config init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite an existing file")
	_ = fs.Parse(args)
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: cbz config init [--force] [file]")
	}
	path := fs.Arg(0)
	if path == "" {
		var err error
		if path, err = config.DefaultPath(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if *force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	// 0644: the config holds no secrets (they live in separate files).
	f, err := os.OpenFile(path, flags, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists (use --force to overwrite)", path)
	} else if err != nil {
		return err
	}
	if _, err := f.WriteString(config.Example); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", Sanitize(path))
	return nil
}
