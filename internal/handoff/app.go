package handoff

import (
	"fmt"
	"io"
)

const defaultMaxBytes = int64(100 << 20)

var Version = "dev"

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "setup":
		return runSetup(args[1:], stdin, stdout)
	case "push":
		return runPush(args[1:], stdout)
	case "pull":
		return runPull(args[1:], stdout)
	case "continue":
		return runContinue(args[1:], stdout)
	case "abort":
		return runAbort(args[1:], stdout)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, Version)
		return nil
	case "help", "--help", "-h":
		printUsage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q (run 'handoff help')", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Handoff transfers uncommitted Git changes through a central server.

Usage:
  handoff setup --server URL
  handoff push [-m MESSAGE] [PATH ...]
  handoff pull ID
  handoff continue ID
  handoff abort ID
  handoff serve
  handoff version`)
}
