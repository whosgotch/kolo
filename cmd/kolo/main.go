// Command kolo runs an org's shared agents.
package main

import (
	"fmt"
	"log"
	"os"
	"runtime/debug"
)

// version is what a host reports to the hub, and nothing stamps it at build
// time: the toolchain has already written down where the binary came from, so
// it is read back out rather than passed in.
var version = "dev"

func init() { stamped() }

// stamped fills in version from whatever the build left behind.
func stamped() {
	// A release build says what it is with -ldflags, and nothing below may
	// argue: at a tagged commit the toolchain still reports the commit, and a
	// binary somebody downloaded should call itself by the tag they chose it
	// from.
	if version != "dev" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	// Built from a checkout: go build or go install of a path here. The
	// commit is read before Main.Version because Main.Version holds a
	// synthesised pseudo-version in this case (v0.0.0-<time>-<commit>), and
	// the commit alone is the half of that anybody reads.
	if revision, dirty, ok := fromVCS(info); ok {
		version = revision
		if dirty {
			version += "-dirty"
		}
		return
	}
	// Released: go install pkg@version, and the proxy knows the tag. A build
	// from the module cache carries no VCS information to have preferred.
	if v := info.Main.Version; v != "" && v != "(devel)" {
		version = v
	}
	// Anything else, go run most often, stays "dev", which it is.
}

// fromVCS is the commit a binary was built from, shortened to what a person
// quotes, and whether the tree had uncommitted changes in it.
func fromVCS(info *debug.BuildInfo) (revision string, dirty, ok bool) {
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" {
		return "", false, false
	}
	return revision[:min(len(revision), 12)], dirty, true
}

type command struct {
	run   func([]string) error
	brief string
}

var commands map[string]command

func init() {
	commands = map[string]command{
		"up":      {run: upCmd, brief: "start a hub and lend this machine to it"},
		"serve":   {run: serveCmd, brief: "run the hub for an org"},
		"invite":  {run: inviteCmd, brief: "make a link that lets someone join"},
		"who":     {run: whoCmd, brief: "say who is in the org"},
		"token":   {run: tokenCmd, brief: "mint credentials"},
		"host":    {run: hostCmd, brief: "lend this machine to the org"},
		"doctor":  {run: doctorCmd, brief: "say what will and will not work on this machine"},
		"version": {run: versionCmd, brief: "say which build this is"},
		"help":    {run: helpCmd, brief: "explain kolo, or one of its commands"},
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
	// Answered as flags too, because that is what people type before they
	// think to look for a command.
	if args[0] == "-v" || args[0] == "--version" || args[0] == "-version" {
		fmt.Println(versionLine())
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
