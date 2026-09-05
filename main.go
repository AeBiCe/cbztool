// Command cb is a CLI for inspecting and editing comic book (.cbz) archives.
//
// A .cbz is a plain ZIP archive of page images, optionally containing a
// ComicInfo.xml metadata file. cb deliberately supports only .cbz (ZIP);
// .cbr and .cb7 are out of scope.
//
// The command handlers live in internal/command; this file is just dispatch.
package main

import (
	"errors"
	"fmt"
	"os"

	"cb/internal/command"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		// The error chain can carry untrusted text (filenames, archive entry
		// names, provider responses); neutralize terminal escapes before printing.
		fmt.Fprintln(os.Stderr, "cb:", command.Sanitize(err.Error()))
		os.Exit(1)
	}
}

func run(args []string) error {
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
	case "pack":
		return command.Pack(rest)
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command: %q", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `cb — comic book (.cbz) tool

Usage:
  cb <command> [flags] <path>

Commands:
  list, ls      print archive contents
  validate, vl  check for missing pages
  info          show ComicInfo.xml metadata
  tag           write ComicInfo.xml from a provider (or --manual to prompt)
  strip         remove ComicInfo.xml
  pack          build a .cbz from a directory of images (--tag to also tag)

<path> is a single .cbz file or a directory of .cbz files (pack takes an
image directory). Run a command with -h for its flags.
`)
}
