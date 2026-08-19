// Command kolo runs an org's shared agents.
//
//	kolo up        a hub and this machine lending itself to it
//	kolo invite    a link that turns whoever opens it into a member
//	kolo who       who is in the org, and how they got in
//	kolo serve     the hub an org connects to
//	kolo token     mint credentials
//	kolo host      lend this machine to the org
//
// Agents run on a machine somebody lends to the org; everyone else reaches them
// through the hub, in a browser. See kolo help.
package main

import (
	"fmt"
	"log"
	"os"
)

// Reported to the hub, so a member running something ancient can be identified
// rather than guessed at.
const version = "dev"

type command struct {
	run   func([]string) error
	brief string
}

// Populated in init rather than as a literal: help is one of the commands and
// reads the list, which the compiler will not allow a literal to do.
var commands map[string]command

func init() {
	commands = map[string]command{
		"up":     {run: upCmd, brief: "start a hub and lend this machine to it"},
		"serve":  {run: serveCmd, brief: "run the hub for an org"},
		"invite": {run: inviteCmd, brief: "make a link that lets someone join"},
		"who":    {run: whoCmd, brief: "say who is in the org"},
		"token":  {run: tokenCmd, brief: "mint credentials"},
		"host":   {run: hostCmd, brief: "lend this machine to the org"},
		"help":   {run: helpCmd, brief: "explain kolo, or one of its commands"},
	}
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("kolo: ")

	args := os.Args[1:]
	// Run with nothing to do, kolo explains itself rather than complaining. It
	// is what somebody types first, and being told off for it is a poor start.
	if len(args) == 0 {
		overview(os.Stdout)
		return
	}
	// The flags people try before they know the command is called help.
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
