package main

import (
	"fmt"
	"os"

	handoffapp "handoff/internal/handoff"
)

func main() {
	if err := handoffapp.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if !handoffapp.IsReportedError(err) {
			fmt.Fprintln(os.Stderr, "handoff:", err)
		}
		os.Exit(1)
	}
}
