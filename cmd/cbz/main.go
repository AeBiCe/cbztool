// Command cbz is a CLI for inspecting and editing comic book (.cbz) archives.
//
// A .cbz is a plain ZIP archive of page images, optionally containing a
// ComicInfo.xml metadata file. cbz deliberately supports only .cbz (ZIP);
// .cbr and .cb7 are out of scope.
//
// The command handlers live in internal/command; this file is just dispatch.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/AeBiCe/cbztool/internal/command"
	"github.com/AeBiCe/cbztool/internal/term"
	"github.com/AeBiCe/cbztool/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		// The error chain can carry untrusted text (filenames, archive entry
		// names, provider responses); neutralize terminal escapes before printing.
		term.Errorf("%s", command.Sanitize(err.Error()))
		os.Exit(1)
	}
}

func run(args []string) error {
	args, cfgPath, err := configFlag(args)
	if err != nil {
		return err
	}
	if cfgPath != "" {
		if err := command.SetConfigFile(cfgPath); err != nil {
			return err
		}
	}
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "list", "ls":
		return command.List(rest)
	case "validate", "vl":
		return command.Validate(rest)
	case "info":
		return command.Info(rest)
	case "tag":
		return command.Tag(rest)
	case "strip":
		return command.Strip(rest)
	case "clean":
		return command.Clean(rest)
	case "rename":
		return command.Rename(rest)
	case "edit":
		return command.Edit(rest)
	case "dupes":
		return command.Dupes(rest)
	case "stats":
		return command.Stats(rest)
	case "missing":
		return command.Missing(rest)
	case "pack":
		return command.Pack(rest)
	case "config":
		return command.Config(rest)
	case "help", "-h", "--help":
		usage()
		return nil
	case "version", "--version":
		fmt.Println("cbz", version.String())
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command: %q", cmd)
	}
}

// configFlag consumes a leading -c/--config <file> (or -c=<file>), the one
// flag that applies to every command, and returns the remaining arguments.
func configFlag(args []string) (rest []string, path string, err error) {
	if len(args) == 0 {
		return args, "", nil
	}
	switch a := args[0]; {
	case a == "-c" || a == "--config":
		if len(args) < 2 {
			return nil, "", fmt.Errorf("%s needs a file", a)
		}
		return args[2:], args[1], nil
	case strings.HasPrefix(a, "-c=") || strings.HasPrefix(a, "--config="):
		_, v, _ := strings.Cut(a, "=")
		return args[1:], v, nil
	}
	return args, "", nil
}

func usage() {
	fmt.Fprint(os.Stderr, `cbz — comic book (.cbz) tool (cbztool `+version.String()+`)

Usage:
  cbz [-c <config>] <command> [flags] <path>

Commands:
  list, ls      print archive contents
  validate, vl  check page numbering and that every page is a readable image
  info          show ComicInfo.xml metadata
  tag           write ComicInfo.xml online (Metron + ComicVine combined),
                or -m for manual input (prompts, JSON file, -s Field=Value)
  edit          edit one archive's metadata as JSON in $EDITOR
  strip         remove ComicInfo.xml
  clean         remove OS junk (__MACOSX, .DS_Store, …) from archives
  rename        rename archives from their metadata (template in the config, or -t)
  dupes         find archives with identical pages (-r: include subdirectories)
  stats         summarize a library: archives, pages, series, tagged, problems
  missing       list the issues of each series you don't have (--offline: local gaps)
  pack          build a .cbz from a directory of images (--tag to also tag)
  config        show or create the configuration (path, show, init)
  version       print the version (also --version)

<path> is a single .cbz file or a directory of .cbz files (pack takes an
image directory). Run a command with -h for its flags.

-c, --config <file> layers a config file over the default
($XDG_CONFIG_HOME/cbz/config.json); CBZ_CONFIG does the same.
`)
}
