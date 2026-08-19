package hub

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// TLS is what the hub needs to serve https itself.
//
// A member authenticates with a token in a header, and over plain http that
// header crosses the network where anyone on the path can take it and use it —
// and using it means driving an agent with the host user's whole account. The
// answer used to be "put it behind something that terminates TLS", which is a
// second thing to deploy and the step that makes one command untrue.
type TLS struct {
	// Domains this hub answers for. The certificate is refused for anything
	// else, so a stranger cannot make the hub ask for a certificate in their
	// name.
	Domains []string
	// Cache is where certificates are kept between restarts. Losing it means
	// asking for new ones, and Let's Encrypt counts how often that happens.
	Cache string
	// ChallengeAddr is where Let's Encrypt is answered, and is :80 unless
	// something in front is forwarding that port somewhere else. The tests bind
	// an arbitrary one, a privileged port being a poor thing to need in order
	// to run them.
	ChallengeAddr string
	// Staging asks Let's Encrypt's test service instead. Its certificates are
	// not trusted by browsers, which is the point: the rate limit that matters
	// is on the real one, and a setup is got wrong a few times before it is got
	// right.
	Staging bool
}

// DefaultCache is beside the rest of what a machine remembers about kolo.
func DefaultCache() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "kolo-certs"
	}
	return filepath.Join(dir, "kolo", "certs")
}

// letsEncryptStaging issues untrusted certificates against generous limits.
const letsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"

// Secure makes the hub get and renew its own certificate. It must be called
// before Serve.
//
// Two ports, not one: 443 carries the org, and 80 is where Let's Encrypt
// connects to check this machine really answers for the domain. Anything else
// arriving on 80 is redirected, so a member who types the bare name still ends
// up somewhere encrypted.
func (s *Server) Secure(cfg TLS) error {
	if len(cfg.Domains) == 0 {
		return fmt.Errorf("hub: no domain to get a certificate for")
	}
	for _, d := range cfg.Domains {
		if strings.Contains(d, "/") || strings.Contains(d, ":") || net.ParseIP(d) != nil {
			return fmt.Errorf("hub: %q is not a domain name: a certificate is issued for a name, and a name is what people type", d)
		}
	}
	if cfg.Cache == "" {
		cfg.Cache = DefaultCache()
	}
	// Made now rather than on the first request, so a directory that cannot be
	// written is a refusal at startup instead of a certificate fetched again on
	// every restart until somebody notices the rate limit.
	if err := os.MkdirAll(cfg.Cache, 0o700); err != nil {
		return fmt.Errorf("hub: certificate cache: %w", err)
	}

	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.Domains...),
		Cache:      autocert.DirCache(cfg.Cache),
	}
	if cfg.Staging {
		m.Client = &acme.Client{DirectoryURL: letsEncryptStaging}
	}

	// Bound here, not in a goroutine at Serve: port 80 is privileged and often
	// taken, and "kolo needs to be able to bind :80" is something to be told at
	// startup rather than to find in a log after the certificate never arrives.
	if cfg.ChallengeAddr == "" {
		cfg.ChallengeAddr = ":80"
	}
	challenges, err := net.Listen("tcp", cfg.ChallengeAddr)
	if err != nil {
		return fmt.Errorf("hub: %w\n\nPort 80 is where Let's Encrypt checks this machine answers for %s.\n"+
			"Something else may be on it, or this user may not be allowed to bind it.",
			err, strings.Join(cfg.Domains, ", "))
	}
	s.challenges = challenges
	s.acme = m
	s.srv.TLSConfig = &tls.Config{
		GetCertificate: m.GetCertificate,
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
	}
	return nil
}

// serveChallenges answers Let's Encrypt on port 80 and sends everybody else to
// the encrypted address.
func (s *Server) serveChallenges() {
	srv := &http.Server{Handler: s.acme.HTTPHandler(nil)}
	if err := srv.Serve(s.challenges); err != nil && !isClosed(err) {
		log.Printf("hub: port 80: %v", err)
	}
}

// AlsoServe adds a second listener, serving plain http.
//
// It exists for a caller on this machine — kolo up runs a host in the same
// process — which would otherwise have to go out to the domain and come back to
// reach a hub that is only listening for https. Whether that works depends on
// the network answering its own name, which is a thing to debug rather than a
// thing to rely on.
//
// Plain http is not a weakness here: nothing on this listener leaves the
// machine. Bind it to loopback and keep it that way.
//
// Like Secure, it must be called before Serve: a listener added afterwards
// would be bound and then never read from, which is worse than being told no.
func (s *Server) AlsoServe(addr string) (string, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.serving {
		return "", fmt.Errorf("hub: AlsoServe must be called before Serve")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("hub: listen on %s: %w", addr, err)
	}
	s.extra = append(s.extra, ln)
	return ln.Addr().String(), nil
}
