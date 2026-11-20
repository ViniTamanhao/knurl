// Command knurl is the CLI and stdio MCP server entrypoint for the knurl tool vault.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ViniTamanhao/knurl/internal/runtime"
)

var version = "0.1.0-dev"

const helpText = `knurl — Tools your agents forge. Typed to run, safe to keep, zero shell required.

Usage:
  knurl <command> [flags]

Commands:
  run <tool-name-or-path>   Execute a tool bundle in an isolated worker subprocess
  mcp                       Start the five-tool stdio MCP server
  approve [id|--last]        Review and explicitly approve a staged proposal
  reject [id|--last]         Reject a proposal (--reason <text>)
  rollback <tool>            Restore a version (--to vN, --scope repo|global)
  proposals                 List proposals (--status pending|all, --json)
  version                   Print knurl version information
  help                      Show this help message

Flags:
  -h, --help                Show this help message
  -v, --version             Print knurl version information
`

func main() {
	os.Exit(runWithStdin(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithStdin(args, os.Stdin, stdout, stderr)
}

func runWithStdin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeOut(stdout, helpText, stderr)
	}

	switch args[0] {
	case "_worker":
		return runtime.RunWorker(stdin, stdout)
	case "approve", "reject", "rollback", "proposals":
		return runLifecycleCmd(args[0], args[1:], stdin, stdout, stderr)
	case "run":
		return runToolCmd(args[1:], stdout, stderr)
	case "mcp":
		return runMCPCmd(args[1:], stdout, stderr)
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
