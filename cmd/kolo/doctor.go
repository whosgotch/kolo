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

// How long an agent may go unrecognised before it is worth saying so. Long
// enough that an agent still starting up is not a complaint, short enough that
// somebody watching a first run finds out in that sitting.
const puzzled = 2 * time.Minute

// doctorCmd says what will and will not work on this machine.
//
// It changes nothing: no agent is touched, no file is written, nothing is
// started. A diagnostic that alters what it is diagnosing is worse than none.
//
// It reads what the host wrote down rather than asking anything, so it works on
// a machine where kolo is running and on one where it is not, and needs none of
// the flags kolo up was given.
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
		// Something here needs a person. Said with the exit code as well as on
		// screen, so this can be the last line of a setup script.
		os.Exit(1)
	}
	return nil
}

// doctor writes the report and says whether everything it looked at was well.
func doctor(w io.Writer, statePath, kindsPath string) (bool, error) {
	if _, err := adapter.Load(kindsPath); err != nil {
		// A kinds file kolo will not read is the whole diagnosis: the host
		// refuses to start on it too.
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

// The verdicts. A failure is something that will not work; a warning is
// something that works and is probably not what somebody meant.
const (
	good = "ok  "
	warn = "note"
	bad  = "fail"
)

// runnable says whether what the org may start can be started at all. It is
// checked here because the person who finds out otherwise is a member in a
// browser, and the person who can fix it is whoever lent the machine.
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

// shown names the command, and where it was found when that is not obvious.
func shown(command, path, program string) string {
	if path == program || filepath.Base(path) == command {
		return command
	}
	return command + "  (" + path + ")"
}

// knownKinds says what kolo can do with each of them. This is the answer to the
// question a host actually has, which is not whether an agent is supported but
// what will and will not work if they lend it.
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

		// Watching and typing need nothing: they are the terminal, and the
		// terminal is the same whatever is drawing on it.
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

// missing says what each gap costs, in the terms of somebody using the agent
// rather than in the terms of the file that would fix it.
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

// eachAgent reports what this machine was last running, and what kolo made of each
// screen. Free to check and the only thing that catches an agent whose markers
// stopped fitting it — a CLI that shipped a new footer breaks nothing that says
// so, until somebody presses stop and nothing happens.
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
			// Only once it has had time to draw something. An agent still
			// starting has said nothing yet, which is not a fault.
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

// since is a duration a person would say out loud.
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
