package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/config"
	"github.com/whosgotch/kolo/internal/host"
	"github.com/whosgotch/kolo/internal/hub"
)

// puzzled is how long an agent may go unrecognised before it counts as a fault.
const puzzled = 2 * time.Minute

func doctorCmd(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	statePath := fs.String("state", config.Path("agents.json"), "what this machine wrote down about its agents")
	kindsPath := fs.String("kinds", config.Path("kinds.json"), "agent kinds this machine knows beyond the ones kolo ships with")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo doctor")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nSays what this machine can and cannot do with the agents it lends,")
		fmt.Fprintln(os.Stderr, "and whether kolo can read what any of them are doing. It changes")
		fmt.Fprintln(os.Stderr, "nothing.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	ok, err := doctor(os.Stdout, *statePath, *kindsPath)
	if err != nil {
		return err
	}
	if !ok {
		os.Exit(1)
	}
	return nil
}

func doctor(w io.Writer, statePath, kindsPath string) (bool, error) {
	if _, err := adapter.Load(kindsPath); err != nil {
		fmt.Fprintf(w, "%s %v\n", bad, err)
		return false, nil
	}
	state, err := host.ReadState(statePath)
	if err != nil {
		fmt.Fprintf(w, "%s %v\n", bad, err)
		return false, nil
	}
	if len(state.Allows) == 0 && len(state.Agents) == 0 {
		fmt.Fprintf(w, "This machine has not lent itself to an org yet, so there is nothing\n"+
			"to check. Run kolo up in a directory you want the org to work in.\n")
		return true, nil
	}

	well := runnable(w, state.Allows)
	knownKinds(w, state.Allows)
	return eachAgent(w, state.Agents) && well, nil
}

const (
	good = "ok  "
	warn = "note"
	bad  = "fail"
)

func runnable(w io.Writer, allows []string) bool {
	fmt.Fprintf(w, "what this machine runs\n")
	well := true
	for _, command := range allows {
		// The wildcard is a decision rather than a program, and it is well by
		// definition: what it lends is whatever turns out to be on PATH.
		if command == hub.AllowAny {
			fmt.Fprintf(w, "  %s *  (any command found on PATH)\n", good)
			continue
		}
		argv := adapter.Argv(command)
		if len(argv) == 0 {
			continue
		}
		path, err := exec.LookPath(argv[0])
		switch {
		case err != nil:
			fmt.Fprintf(w, "  %s %s\n      %s is not on PATH, so an agent asked for here will fail to start\n",
				bad, command, argv[0])
			well = false
		default:
			fmt.Fprintf(w, "  %s %s\n", good, shown(command, path, argv[0]))
		}
	}
	fmt.Fprintln(w)
	return well
}

func shown(command, path, program string) string {
	if path == program || filepath.Base(path) == command {
		return command
	}
	return command + "  (" + path + ")"
}

func knownKinds(w io.Writer, allows []string) {
	fmt.Fprintf(w, "what kolo knows about them\n")
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(table, "  agent\twatch\ttype\tstop\treads screen\tresume\n")

	var notes []string
	rows := 0
	for _, command := range allows {
		// '*' names no agent: what it lends is whatever the org starts, and
		// each of those is described by its own kind or by nothing.
		if command == hub.AllowAny {
			continue
		}
		rows++
		argv := adapter.Argv(command)
		if len(argv) == 0 {
			continue
		}
		name := filepath.Base(argv[0])
		kind := adapter.For(command)

		stop, reads, resume := no, no, no
		if kind.Markers.Busy != "" {
			stop = yes
		}
		if !kind.Markers.Blank() {
			reads = yes
		}
		if len(kind.Resume) > 0 {
			resume = strings.Join(kind.Resume, " ")
		}
		fmt.Fprintf(table, "  %s\t%s\t%s\t%s\t%s\t%s\n", name, yes, yes, stop, reads, resume)
		notes = append(notes, missing(name, kind)...)
	}
	table.Flush()
	if rows == 0 {
		fmt.Fprintf(w, "  (whatever the org starts; kinds.json describes one so its screen can be read)\n")
	}
	for _, note := range notes {
		fmt.Fprintf(w, "      %s\n", note)
	}
	fmt.Fprintln(w)
}

const (
	yes = "yes"
	no  = "-"
)

func missing(name string, kind adapter.Adapter) []string {
	if kind.Markers.Blank() && len(kind.Resume) == 0 {
		return []string{name + " runs and is shared, but kolo cannot read its screen: the list will" +
			"\n      not say what it is doing, nobody can stop it from the browser, and it" +
			"\n      starts fresh every restart. Describe it in kinds.json — see docs/hub.md."}
	}
	var notes []string
	if kind.Markers.Busy == "" {
		notes = append(notes, name+" has no busy marker, so stop is refused on every screen:"+
			"\n      the only way to interrupt it is to take its keyboard.")
	}
	if len(kind.Resume) == 0 {
		notes = append(notes, name+" has no resume command, so every restart of one starts fresh.")
	}
	return notes
}

func eachAgent(w io.Writer, records []host.Record) bool {
	fmt.Fprintf(w, "agents this machine was running\n")
	if len(records) == 0 {
		fmt.Fprintf(w, "  none\n")
		return true
	}
	well := true
	for _, rec := range records {
		for_ := ""
		if !rec.Since.IsZero() {
			for_ = " for " + since(rec.Since)
		}
		switch {
		case rec.State == "" || rec.State == "unknown":
			if !rec.Since.IsZero() && time.Since(rec.Since) < puzzled {
				fmt.Fprintf(w, "  %s %s  starting, nothing on its screen yet\n", good, rec.Spec.Name)
				continue
			}
			fmt.Fprintf(w, "  %s %s  its screen has said nothing kolo understands%s\n"+
				"      the markers for %s do not fit what it is drawing. Nothing else\n"+
				"      would have told you: watching and typing work either way.\n",
				warn, rec.Spec.Name, for_, filepath.Base(firstWord(rec.Spec.Command)))
			well = false
		default:
			fmt.Fprintf(w, "  %s %s  %s%s\n", good, rec.Spec.Name, rec.State, for_)
		}
	}
	return well
}

func firstWord(command string) string {
	if argv := adapter.Argv(command); len(argv) > 0 {
		return argv[0]
	}
	return command
}

// since renders time since t the way a person would say it.
func since(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	}
	return plural(int(d.Hours()/24), "day")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
