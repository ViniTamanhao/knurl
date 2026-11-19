// Command knurl is the CLI and stdio MCP server entrypoint for the knurl tool vault.
package main

import (
	"fmt"
	"io"
	"os"
)

var version = "0.0.0-dev"

const helpText = `knurl — Tools your agents forge. Typed to run, safe to keep, zero shell required.

Usage:
  knurl [flags]

Flags:
  -h, --help      Show this help message
  -v, --version   Print knurl version information

Status:
  Phase 0 (Foundation, Contracts & Spec Freeze). Runtime subcommands arrive in Phase 1+.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeOut(stdout, helpText, stderr)
	}

	switch args[0] {
	case "-h", "--help", "help":
		return writeOut(stdout, helpText, stderr)
	case "-v", "--version", "version":
		return writeOut(stdout, fmt.Sprintf("knurl %s\n", version), stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "knurl: unknown command or flag %q (run 'knurl --help')\n", args[0])
		return 2
	}
}

func writeOut(w io.Writer, text string, stderr io.Writer) int {
	if _, err := io.WriteString(w, text); err != nil {
		_, _ = fmt.Fprintf(stderr, "knurl: write output: %v\n", err)
		return 1
	}
	return 0
}
