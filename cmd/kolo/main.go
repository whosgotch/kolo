// Command kolo runs an org's shared agents.
//
//	kolo serve     the hub an org connects to
//	kolo token     mint credentials
//
// Agents run on a machine somebody lends to the org; everyone else reaches them
// through the hub, in a browser.
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
	"token": {tokenCmd, "mint credentials"},
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
		fmt.Fprintf(os.Stderr, "kolo: no command %q\n\n", args[0])
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
