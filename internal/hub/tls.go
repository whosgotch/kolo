package hub

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/whosgotch/kolo/internal/config"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// TLS configures the hub to obtain and serve its own certificates.
type TLS struct {
	// Certificates are refused for any other name.
	Domains []string
	// Certificate store between restarts; losing it costs Let's Encrypt rate limit.
	Cache string
	// Where ACME challenges are answered; defaults to :80.
	ChallengeAddr string
	// Use Let's Encrypt's staging CA: untrusted certificates, loose limits.
	Staging bool
}

func DefaultCache() string {
	return config.Path("certs")
}

const letsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"

// Secure sets up autocert: 443 serves the hub, 80 answers ACME challenges and
// redirects everything else to https. Must be called before Serve.
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

func (s *Server) serveChallenges() {
	srv := &http.Server{Handler: s.acme.HTTPHandler(nil)}
	if err := srv.Serve(s.challenges); err != nil && !isClosed(err) {
		log.Printf("hub: port 80: %v", err)
	}
}

// AlsoServe adds a plain-http listener for clients in the same process.
// Must be called before Serve.
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
