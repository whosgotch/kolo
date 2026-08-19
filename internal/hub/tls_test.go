package hub

import (
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"testing"
)

// secureHub is a hub set up for https but not yet serving, with the challenge
// listener on a port the tests are allowed to bind.
func secureHub(t *testing.T, domains ...string) *Server {
	t.Helper()
	org := &Org{Name: "acme", Members: []Member{{ID: "artem", TokenHash: HashToken("kolo_x")}}}
	s, err := Listen(org, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Secure(TLS{
		Domains:       domains,
		Cache:         t.TempDir(),
		ChallengeAddr: "127.0.0.1:0",
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

// secureFixture is one already serving, for a test that only dials it.
func secureFixture(t *testing.T, domains ...string) *Server {
	t.Helper()
	s := secureHub(t, domains...)
	go s.Serve()
	return s
}

// The listener has to actually speak TLS. A plain-http listener answers a TLS
// handshake with rubbish rather than refusing it, so this is worth asserting
// rather than assuming.
func TestSecureServesTLS(t *testing.T) {
	s := secureFixture(t, "hub.acme.test")

	// A name this hub does not answer for: the certificate is refused at once,
	// rather than kolo asking a certificate authority for somebody else's name.
	_, err := tls.Dial("tcp", s.Addr(), &tls.Config{
		ServerName:         "stranger.example",
		InsecureSkipVerify: true,
	})
	if err == nil {
		t.Fatal("the hub offered a certificate for a domain it was not given")
	}
	// A TLS alert from the far end, which is what refusing to produce a
	// certificate looks like from here. A listener serving plain http fails
	// differently — it answers a handshake with text, and the client says the
	// first record does not look like TLS.
	if !strings.Contains(err.Error(), "remote error") {
		t.Errorf("refused, but not by a TLS server: %v", err)
	}
}

func TestSecureRefusesWhatCannotHaveACertificate(t *testing.T) {
	for _, domain := range []string{"", "192.168.1.24", "hub.acme.test:7300", "http://hub.acme.test"} {
		org := &Org{Name: "acme"}
		s, err := Listen(org, "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		var domains []string
		if domain != "" {
			domains = []string{domain}
		}
		err = s.Secure(TLS{Domains: domains, Cache: t.TempDir(), ChallengeAddr: "127.0.0.1:0"})
		if err == nil {
			t.Errorf("Secure accepted %q, which cannot have a certificate", domain)
		}
		s.Close()
	}
}

// kolo up runs a host in the same process, and it reaches the hub over
// loopback rather than out to the domain and back.
func TestAlsoServeIsPlainHTTPOnLoopback(t *testing.T) {
	s := secureHub(t, "hub.acme.test")
	addr, err := s.AlsoServe("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	waitFor(t, func() bool {
		_, err := http.Get("http://" + addr + "/v1/agents")
		return err == nil
	})

	resp, err := http.Get("http://" + addr + "/v1/agents")
	if err != nil {
		t.Fatalf("plain http on the second listener: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	// Unauthenticated, so refused — but refused by the hub, which means it was
	// speaking http rather than expecting a handshake.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// The order matters, so getting it wrong is refused rather than quietly
// producing a listener nothing ever reads from.
func TestAlsoServeAfterServeIsRefused(t *testing.T) {
	s := secureFixture(t, "hub.acme.test")
	waitFor(t, func() bool {
		_, err := s.AlsoServe("127.0.0.1:0")
		return err != nil
	})
}
