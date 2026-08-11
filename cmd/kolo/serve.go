package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/whosgotch/kolo/internal/hub"
)

// serveCmd runs the hub for one org.
func serveCmd(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	orgPath := fs.String("org", "org.json", "org file listing members and their token hashes")
	addr := fs.String("addr", "127.0.0.1:7300", "address to listen on; 0.0.0.0:7300 to accept other machines")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo serve [flags]")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nThe hub carries no TLS of its own. Reaching it across a network")
		fmt.Fprintln(os.Stderr, "means putting it behind something that does, or a member's token")
		fmt.Fprintln(os.Stderr, "travels in the clear.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	org, err := hub.Load(*orgPath)
	if err != nil {
		return err
	}
	s, err := hub.Listen(org, *addr)
	if err != nil {
		return err
	}

	log.Printf("hub for %s on %s, %d member(s)", org.Name, s.Addr(), len(org.Members))

	// Shut down on a signal so that agents are disconnected deliberately and
	// come back on their own, rather than being left holding a dead socket.
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopping
		log.Print("stopping")
		s.Close()
	}()

	return s.Serve()
}

// tokenCmd mints credentials for one member.
//
// The token is shown once, here, and never stored by kolo: the hub keeps only a
// hash of it. Losing it means issuing another, which is the intended cost.
func tokenCmd(args []string) error {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	id := fs.String("id", "", "member id, as used by kolo who")
	name := fs.String("name", "", "member's display name (defaults to the id)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo token -id <id> [-name <name>]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		fs.Usage()
		os.Exit(2)
	}
	if *name == "" {
		*name = *id
	}

	token, hash, err := hub.NewToken()
	if err != nil {
		return err
	}
	entry, err := json.MarshalIndent(hub.Member{ID: *id, Name: *name, TokenHash: hash}, "    ", "  ")
	if err != nil {
		return err
	}

	fmt.Printf("Give this to %s, once. It is not stored anywhere:\n\n", *name)
	fmt.Printf("    KOLO_TOKEN=%s\n\n", token)
	fmt.Printf("Add this to the members list in your org file:\n\n    %s\n\n", entry)
	fmt.Println("The hub keeps only the hash, so a lost token is replaced rather than recovered.")
	return nil
}
