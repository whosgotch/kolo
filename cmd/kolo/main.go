// Command kolo runs an org's shared agents.
package main

import (
	"fmt"
	"log"
	"os"
)

// version is reported to the hub and set by the Makefile via -ldflags.
var version = "dev"

type command struct {
	run   func([]string) error
	brief string
}

var commands map[string]command

func init() {
	commands = map[string]command{
		"up":     {run: upCmd, brief: "start a hub and lend this machine to it"},
		"serve":  {run: serveCmd, brief: "run the hub for an org"},
		"invite": {run: inviteCmd, brief: "make a link that lets someone join"},
		"who":    {run: whoCmd, brief: "say who is in the org"},
		"token":  {run: tokenCmd, brief: "mint credentials"},
		"host":   {run: hostCmd, brief: "lend this machine to the org"},
		"doctor": {run: doctorCmd, brief: "say what will and will not work on this machine"},
		"help":   {run: helpCmd, brief: "explain kolo, or one of its commands"},
	}
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("kolo: ")

	args := os.Args[1:]
	if len(args) == 0 {
		overview(os.Stdout)
		return
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "-help" {
		overview(os.Stdout)
		return
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "kolo: no command %q\n\n", args[0])
		overview(os.Stderr)
		os.Exit(2)
	}
	if err := cmd.run(args[1:]); err != nil {
		log.Fatal(err)
	}
}
