package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/whosgotch/kolo/internal/config"
	"github.com/whosgotch/kolo/internal/hub"
)

func serveCmd(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	orgPath := fs.String("org", config.Path("org.json"), "org file listing members and their token hashes")
	addr := fs.String("addr", "", "address to listen on (default 127.0.0.1:7300, or :443 with -tls-domain)")
	tlsDomain := fs.String("tls-domain", "", "get and renew a certificate for this domain, and serve https (repeat, or comma-separated)")
	tlsCache := fs.String("tls-cache", hub.DefaultCache(), "where to keep certificates between restarts")
	tlsStaging := fs.Bool("tls-staging", false, "use Let's Encrypt's test service, whose certificates browsers do not trust")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo serve [flags]")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nWithout -tls-domain the hub serves plain http, and a member's token")
		fmt.Fprintln(os.Stderr, "crosses the network in a header anyone on the path can read. That is a")
		fmt.Fprintln(os.Stderr, "considered choice on a trusted network and a bad one anywhere else.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	org, err := hub.Load(*orgPath)
	if err != nil {
		return err
	}
	domains := split(*tlsDomain)
	if *addr == "" {
		*addr = "127.0.0.1:7300"
		if len(domains) > 0 {
			*addr = ":443"
		}
	}
	s, err := hub.Listen(org, *addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("%w\n\nSomething is already listening there, most likely another kolo serve.\n"+
				"Find it with:   lsof -nP -i:%s\n"+
				"Or use another: kolo serve -org %s -addr %s",
				err, portOf(*addr), *orgPath, nextAddr(*addr))
		}
		return err
	}

	if len(domains) > 0 {
		if err := s.Secure(hub.TLS{Domains: domains, Cache: *tlsCache, Staging: *tlsStaging}); err != nil {
			return err
		}
		log.Printf("hub for %s on %s, https for %s, %d member(s)",
			org.Name, s.Addr(), strings.Join(domains, " "), len(org.Members))
	} else {
		log.Printf("hub for %s on %s, %d member(s)", org.Name, s.Addr(), len(org.Members))
	}

	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopping
		log.Print("stopping")
		s.Close()
	}()

	return s.Serve()
}

// portOf uses net.SplitHostPort: cutting at the first colon breaks on [::]:7300.
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return addr
}

func nextAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	n, convErr := strconv.Atoi(port)
	if err != nil || convErr != nil {
		return addr
	}
	return net.JoinHostPort(host, strconv.Itoa(n+1))
}

func tokenCmd(args []string) error {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	orgPath := fs.String("org", config.Path("org.json"), "org file to add them to")
	id := fs.String("id", "", "member or host id")
	name := fs.String("name", "", "member's display name (defaults to the id)")
	asHost := fs.Bool("host", false, "credentials for a machine that will run agents, not a person")
	hubURL := fs.String("hub", defaultHubURL, "where they will reach the hub")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kolo token -id <id> [-name <name>]")
		fmt.Fprintln(os.Stderr, "       kolo token -host -id <id>")
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

	if *asHost {
		if _, err := hub.AddHost(*orgPath, hub.Host{ID: *id, TokenHash: hash}); err != nil {
			return missingOrg(err, *orgPath)
		}
		fmt.Printf("Added %s to %s.\n\n", *id, *orgPath)
		fmt.Printf("Run this on %s. It carries both the hub and the token, and is stored nowhere:\n\n", *id)
		fmt.Printf("    kolo host -join %s \\\n        -dir <a directory to lend> -allow claude\n\n", hub.NewJoin(*hubURL, token))
		fmt.Printf("It will dial %s. Pass -hub to kolo token if %s meets the hub elsewhere.\n", *hubURL, *id)
		return nil
	}

	if _, err := hub.AddMember(*orgPath, hub.Member{ID: *id, Name: *name, TokenHash: hash}); err != nil {
		return missingOrg(err, *orgPath)
	}
	fmt.Printf("Added %s to %s.\n\n", *name, *orgPath)
	fmt.Printf("Send %s these two, once. The token is stored nowhere:\n\n", *name)
	fmt.Printf("    %s\n    %s\n\n", *hubURL, token)
	fmt.Println("The hub keeps only the hash, so a lost token is replaced rather than recovered.")
	return nil
}

const defaultHubURL = "http://127.0.0.1:7300"

func missingOrg(err error, path string) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return fmt.Errorf("%w\n\nAn org file starts as its name:\n\n    echo '{\"org\": \"acme\"}' > %s", err, path)
}
