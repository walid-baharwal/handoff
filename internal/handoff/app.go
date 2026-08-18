package handoff

import (
	"flag"
	"fmt"
	"io"
)

const defaultMaxBytes = int64(100 << 20)
const defaultMaxStorageBytes = int64(10 << 30)
const defaultMaxUploads = 4

var Version = "dev"

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	command := args[0]
	commandArgs := args[1:]
	var err error
	switch command {
	case "setup":
		err = runSetup(commandArgs, stdin, stdout)
	case "push":
		err = runPush(commandArgs, stdin, stdout)
	case "changes":
		err = runChanges(commandArgs, stdout)
	case "pull":
		err = runPull(commandArgs, stdin, stdout)
	case "list":
		err = runList(commandArgs, stdout)
	case "inspect":
		err = runInspect(commandArgs, stdout)
	case "delete":
		err = runDelete(commandArgs, stdout)
	case "comment":
		err = runComment(commandArgs, stdout)
	case "comments":
		err = runComments(commandArgs, stdout)
	case "audit":
		err = runAudit(commandArgs, stdout)
	case "whoami":
		err = runWhoami(commandArgs, stdout)
	case "read", "unread", "archive", "unarchive", "acknowledge", "applied", "revoke", "assign", "expire":
		action := map[string]string{"acknowledge": "acknowledged"}[command]
		if action == "" {
			action = command
		}
		err = runHandoffEvent(command, action, commandArgs, stdout)
	case "continue":
		err = runContinue(commandArgs, stdout)
	case "abort":
		err = runAbort(commandArgs, stdout)
	case "status":
		err = runStatus(commandArgs, stdout)
	case "serve":
		err = runServe(commandArgs, stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, Version)
		return nil
	case "help", "--help", "-h":
		printUsage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q (run 'handoff help')", args[0])
	}
	if err != nil && commandWantsJSON(command, commandArgs) {
		return reportIntegrationError(stderr, command, err)
	}
	return err
}

func newSilentFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Handoff transfers uncommitted Git changes through a central server.

Usage:
  handoff setup --server URL [--token-stdin]
  handoff push [-m MESSAGE] [--to USER] [--team TEAM] [--private] [--dry-run] [--json] [--interactive] [--staged|--worktree] [--exclude PATH] [PATH ...]
  handoff changes [--json]
  handoff list [--all] [--sent] [--archived] [--limit N] [--offset N] [--json]
  handoff inspect [--json] ID
  handoff pull [--dry-run] [--yes] [--json] [ID]
  handoff status [--json]
  handoff delete ID
  handoff comment ID MESSAGE
  handoff comments [--json] ID
  handoff acknowledge|applied|revoke|archive|unarchive|read|unread [--json] ID
  handoff assign --target USER ID
  handoff expire --expires-at RFC3339 ID
  handoff audit [--json] ID
  handoff whoami [--json]
  handoff continue ID
  handoff abort ID
  handoff serve
  handoff version`)
}
