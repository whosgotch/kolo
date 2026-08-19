package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/whosgotch/kolo/internal/adapter"
	"github.com/whosgotch/kolo/internal/host"
	"github.com/whosgotch/kolo/internal/hub"
)

// upCmd is the whole of kolo in one command: the hub the org connects to, and
// this machine lending itself to that hub. Everything it needs that does not
// exist yet — the org file, this machine's credential, the first member — it
// makes, so getting started is one line and nothing to paste between steps.
//
// serve, token and host remain for an org whose hub lives somewhere other than
// the machine running the agents. That is the deployment; this is the default.
func upCmd(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	var dirs, allow list
	fs.Var(&dirs, "dir", "a directory the org may run agents in (repeat for more; default the current directory)")
	fs.Var(&allow, "allow", "an agent command the org may run (repeat, or comma-separated; default whichever kolo knows and finds installed)")
	orgPath := fs.String("org", "org.json", "org file, created if it is not there")
	name := fs.String("name", "", "org name, used only when creating the org file (default this directory's name)")
	addr := fs.String("addr", "0.0.0.0:7300", "address the hub listens on; 127.0.0.1:7300 to keep it to this machine")
	state := fs.String("state", defaultState(), "where to record the agents running here")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo up [-dir <path>...] [-allow <command>]")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nStarts a hub and lends this machine to it. What the org may run here")
		fmt.Fprintln(os.Stderr, "is bounded by -dir and -allow, but a running agent has this user's")
		fmt.Fprintln(os.Stderr, "whole account. Run it as a user that owns only what the org should have.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Both of these are refusals in kolo host, where the machine being lent is
	// deliberate and saying so costs nothing. Here they are the common case, so
	// they are answered rather than asked.
	if len(dirs) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		dirs = list{cwd}
	}
	if err := resolveDirs(dirs); err != nil {
		return err
	}
	if len(allow) == 0 {
		allow = installedKinds()
		if len(allow) == 0 {
			return fmt.Errorf("no agent command found: kolo knows %s, and none of them are on PATH\n"+
				"Install one, or name it with -allow if it lives somewhere else",
				strings.Join(adapter.Kinds(), ", "))
		}
	}

	// The org, this machine and the first member all have to be on disk before
	// the hub starts: it reads the file once and never again.
	created, err := hub.Init(*orgPath, orgName(*name))
	if err != nil {
		return err
	}
	id, err := machineID()
	if err != nil {
		return err
	}
	// A fresh token every start, replacing the last. Nothing keeps this one —
	// it goes straight to the host half in memory — so there is nothing to lose
	// and nothing left behind by a start that crashed.
	hostToken, hostHash, err := hub.NewToken()
	if err != nil {
		return err
	}
	org, err := hub.SetHost(*orgPath, hub.Host{ID: id, TokenHash: hostHash})
	if err != nil {
		return err
	}
	var firstToken string
	if len(org.Members) == 0 {
		me := whoami()
		token, hash, err := hub.NewToken()
		if err != nil {
			return err
		}
		if org, err = hub.AddMember(*orgPath, hub.Member{ID: me, Name: me, TokenHash: hash}); err != nil {
			return err
		}
		firstToken = token
	}

	s, err := hub.Listen(org, *addr)
	if err != nil {
		return err
	}
	defer s.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		s.Close()
	}()

	// The host half dials the hub half over loopback rather than being called
	// directly, so it is the same host talking to the same hub as it would be on
	// two machines. One process is a deployment, not a second code path.
	agents := host.NewAgents(host.Config{
		Hub:     "http://" + net.JoinHostPort("127.0.0.1", portOf(s.Addr())),
		Token:   hostToken,
		Dirs:    dirs,
		Allow:   allow,
		Version: version,
	}, *state)
	defer agents.StopAll()

	served := make(chan error, 1)
	go func() { served <- s.Serve() }()

	if created {
		fmt.Printf("Created %s for %s.\n", *orgPath, org.Name)
	}
	fmt.Printf("\n%s is up at %s\n", org.Name, browseURL(s.Addr()))
	fmt.Printf("Lending %s, running %s.\n", strings.Join(dirs, " "), strings.Join(allow, " "))
	if firstToken != "" {
		fmt.Printf("\nOpen it and sign in with this. It is stored nowhere, so keep it:\n\n    %s\n", firstToken)
	}
	fmt.Printf("\nAdd someone:  kolo token -org %s -id <id> -name <name> -hub %s\n", *orgPath, browseURL(s.Addr()))
	fmt.Printf("The hub reads %s once, so restart this to let them in.\n", *orgPath)
	if !onLoopback(*addr) {
		fmt.Printf("\nTokens cross the network in a header, and this hub has no TLS: on anything\n" +
			"but a trusted network, put it behind something that terminates TLS.\n")
	}
	fmt.Println()

	if err := agents.Restore(); err != nil {
		log.Print(err)
	}
	if names := agents.Names(); len(names) > 0 {
		log.Printf("brought back %s", strings.Join(names, " "))
	}
	host.Run(ctx, agents, func(e host.Event) {
		switch {
		case e.Connected:
			log.Printf("lending %s to %s", e.Host, e.Org)
		case e.Err != nil:
			log.Printf("%v; retrying in %s", e.Err, e.Retry.Round(100_000_000))
		}
	})
	// host.Run returns when the signal arrives, which is also what closes the
	// hub; a Serve that ended on its own is the interesting error.
	select {
	case err := <-served:
		return err
	default:
		return nil
	}
}

// installedKinds is every agent kolo knows about that this machine can actually
// run, so the common case names none of them.
func installedKinds() list {
	var found list
	for _, kind := range adapter.Kinds() {
		if _, err := exec.LookPath(kind); err == nil {
			found = append(found, kind)
		}
	}
	return found
}

// orgName falls back to the directory kolo was started in. It is a label, shown
// in the browser and the log, and one that can be corrected by editing a line.
func orgName(given string) string {
	if given != "" {
		return given
	}
	if cwd, err := os.Getwd(); err == nil {
		if base := filepath.Base(cwd); base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	return "kolo"
}

// machineID is what this host appears as in the log. The hostname, because that
// is the name the org already has for the machine.
func machineID() (string, error) {
	name, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("this machine has no hostname to be known by; use kolo host -join with an id you choose")
	}
	// Trailing .local and the like: the machine is "devbox" to everyone talking
	// about it, and an id is read by people.
	if base, _, ok := strings.Cut(name, "."); ok {
		name = base
	}
	return name, nil
}

func whoami() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "owner"
}

// browseURL is the address to hand to a person. A hub on 0.0.0.0 is reachable at
// this machine's address on the network, which is what its org needs, and never
// at 0.0.0.0.
func browseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if lan := lanAddr(); lan != "" {
			host = lan
		} else {
			host = "127.0.0.1"
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

// lanAddr is this machine's address on the network it is on, or empty if it
// appears to be on none.
func lanAddr() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil {
			continue
		}
		return n.IP.String()
	}
	return ""
}

func onLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
