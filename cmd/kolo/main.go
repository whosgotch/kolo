// Command kolo runs a CLI AI agent and connects it to an org.
//
//	kolo serve     the hub an org connects to
//	kolo token     mint a member's credentials
//	kolo run       run an agent here, joined to the hub
//	kolo who       who is connected
//
// An agent runs on the machine of the person using it, with their files and
// their permissions. The hub holds who belongs to the org and who is connected;
// it never reaches back into anyone's machine.
package main

import (
	"fmt"
	"log"
	"os"
	"sort"
)

// version is reported to the hub, so that a member running something ancient
// can be identified as such rather than guessed at.
const version = "dev"

var commands = map[string]struct {
	run   func([]string) error
	brief string
}{
	"serve": {serveCmd, "run the hub for an org"},
	"token": {tokenCmd, "mint a member's credentials"},
	"run":   {runCmd, "run an agent here and join the hub"},
	"who":   {whoCmd, "list who is connected"},
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("kolo: ")

	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	cmd, ok := commands[args[0]]
	if !ok {
		// `kolo claude` used to run an agent, and the muscle memory outlives
		// the change, so say what to type rather than only that this is wrong.
		fmt.Fprintf(os.Stderr, "kolo: no command %q\n\n", args[0])
		fmt.Fprintf(os.Stderr, "did you mean:  kolo run %s\n\n", args[0])
		usage()
		os.Exit(2)
	}
	if err := cmd.run(args[1:]); err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: kolo <command> [flags]")
	fmt.Fprintln(os.Stderr)
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %-7s %s\n", name, commands[name].brief)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "kolo <command> -h for a command's flags")
}
