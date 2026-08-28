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

// lookPath is exec.LookPath behind a var, so a test can say what is installed
// rather than inherit whoever's machine it runs on. Doctor's whole subject is
// the machine underneath it, which is the one thing a test cannot bring along.
var lookPath = exec.LookPath

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
	// First, so a pasted report says which build produced it.
	fmt.Fprintf(w, "%s\n\n", versionLine())
	if _, err := adapter.Load(kindsPath); err != nil {
		fmt.Fprintf(w, "%v\n", err)
		return false, nil
	}
	state, err := host.ReadState(statePath)
	if err != nil {
		fmt.Fprintf(w, "%v\n", err)
		return false, nil
	}
	if len(state.Allows) == 0 && len(state.Agents) == 0 {
		fmt.Fprintf(w, "This machine has not lent itself to an org yet, so there is nothing\n"+
			"to check. Run kolo up in a directory you want the org to work in.\n")
		return true, nil
	}

	well := lends(w, state.Allows, kindsPath)
	return running(w, state.Agents) && well, nil
}

// lends is one line per command the org may start here: whether it is there
// at all, and which of the three things that vary (showing what an agent is
// doing, stopping it, resuming it after a restart) kolo can do with it.
// Watching and typing need nothing, so they are not worth a column.
func lends(w io.Writer, allows []string, kindsPath string) bool {
	fmt.Fprintf(w, "what this machine lends\n")
	well := true
	var unreadable, missing []string
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, command := range allows {
		// The wildcard is a decision rather than a program, and it is well by
		// definition: what it lends is whatever turns out to be on PATH.
		if command == hub.AllowAny {
			fmt.Fprintf(table, "  *\tok\tany command found on PATH\n")
			continue
		}
		argv := adapter.Argv(command)
		if len(argv) == 0 {
			continue
		}
		path, err := lookPath(argv[0])
		if err != nil {
			// Why it matters goes under the table: a long command name in a
			// cell stretches the column for every row above and below it.
			fmt.Fprintf(table, "  %s\tmissing\n", command)
			missing = append(missing, argv[0])
			well = false
			continue
		}
		name, kind := filepath.Base(argv[0]), adapter.For(command)
		if kind.Markers.Blank() && len(kind.Resume) == 0 {
			fmt.Fprintf(table, "  %s\tlimited\twatch and type only\n", shown(command, path, argv[0]))
			unreadable = append(unreadable, name)
			continue
		}
		fmt.Fprintf(table, "  %s\t%s\t%s\n", shown(command, path, argv[0]), verdict(kind), can(kind))
	}
	table.Flush()

	if len(missing) > 0 {
		fmt.Fprintln(w)
		wrap(w, "  ", fmt.Sprintf("%s %s not on PATH, so an agent asked for here will fail to start. "+
			"Install %s, or take %s off what this machine lends.",
			english(missing), verb(missing, "is", "are"),
			verb(missing, "it", "them"), verb(missing, "it", "them")))
	}
	// Once, naming them, rather than the same three lines under every agent
	// that happens to be unknown.
	if len(unreadable) > 0 {
		fmt.Fprintln(w)
		wrap(w, "  ", fmt.Sprintf("%s %s screens kolo does not know, so the list will not say what %s doing, "+
			"nobody can stop one from the browser, and each restart starts it fresh. "+
			"Describe one in %s. See docs/reference.md.",
			english(unreadable), verb(unreadable, "draws", "draw"), verb(unreadable, "it is", "they are"), kindsPath))
	}
	fmt.Fprintln(w)
	return well
}

// verdict is ok when nothing about this kind is missing, partial when
// something is: the row itself says which.
func verdict(kind adapter.Adapter) string {
	if kind.Markers.Blank() || kind.Markers.Busy == "" || len(kind.Resume) == 0 {
		return "partial"
	}
	return "ok"
}

// can lists the three that vary, each either working or plainly not.
func can(kind adapter.Adapter) string {
	parts := []string{"status", "stop", "resume"}
	if kind.Markers.Blank() {
		parts[0] = "no status"
	}
	if kind.Markers.Busy == "" {
		parts[1] = "no stop"
	}
	if len(kind.Resume) == 0 {
		parts[2] = "no resume"
	} else {
		parts[2] = "resume (" + strings.Join(kind.Resume, " ") + ")"
	}
	return strings.Join(parts, " · ")
}

func shown(command, path, program string) string {
	if path == program || filepath.Base(path) == command {
		return command
	}
	return command + "  (" + path + ")"
}

// running is what this machine last wrote down about its own agents: the one
// place markers that stopped fitting an upgraded CLI show up.
func running(w io.Writer, records []host.Record) bool {
	fmt.Fprintf(w, "what it is running\n")
	if len(records) == 0 {
		fmt.Fprintf(w, "  nothing right now\n")
		return true
	}
	well := true
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	var lost []string
	for _, rec := range records {
		held := ""
		if !rec.Since.IsZero() {
			held = " for " + since(rec.Since)
		}
		switch {
		case rec.State != "" && rec.State != "unknown":
			fmt.Fprintf(table, "  %s\t%s%s\n", rec.Spec.Name, rec.State, held)
		// A kind nobody described has no markers to stop fitting, so an
		// unread screen is the limit lends already named, not a fault. Said
		// here too, because "starting, nothing on its screen yet" promises a
		// state that is never coming.
		case adapter.For(rec.Spec.Command).Markers.Blank():
			fmt.Fprintf(table, "  %s\trunning%s, and kolo does not read this kind\n", rec.Spec.Name, held)
		case !rec.Since.IsZero() && time.Since(rec.Since) < puzzled:
			fmt.Fprintf(table, "  %s\tstarting, nothing on its screen yet\n", rec.Spec.Name)
		default:
			fmt.Fprintf(table, "  %s\tits screen has said nothing kolo understands%s\n", rec.Spec.Name, held)
			lost = append(lost, filepath.Base(firstWord(rec.Spec.Command)))
			well = false
		}
	}
	table.Flush()
	if len(lost) > 0 {
		fmt.Fprintln(w)
		wrap(w, "  ", fmt.Sprintf("The markers for %s do not fit what %s drawing. Nothing else would have "+
			"told you: watching and typing work either way.",
			english(unique(lost)), verb(unique(lost), "it is", "they are")))
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
