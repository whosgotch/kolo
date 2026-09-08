// Command kolo runs an org's shared agents.
package main

import (
	"fmt"
	"log"
	"os"
	"runtime/debug"
)

// What a host reports to the hub, read back out of the build rather than
// passed in.
var version = "dev"

func init() { stamped() }

func stamped() {
	// A release build says what it is with -ldflags, and nothing below may
	// argue: at a tagged commit the toolchain still reports the commit.
	if version != "dev" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	// Built from a checkout. The commit is read before Main.Version, which
	// here holds a pseudo-version whose readable half is the commit.
	if revision, dirty, ok := fromVCS(info); ok {
		version = revision
		if dirty {
			version += "-dirty"
		}
		return
	}
	// Released: go install pkg@version, and the proxy knows the tag.
	if v := info.Main.Version; v != "" && v != "(devel)" {
		version = v
	}
}

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
	// As flags too: that is what people type first.
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
