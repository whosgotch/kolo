package hub

import (
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"testing"
)

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

func secureFixture(t *testing.T, domains ...string) *Server {
	t.Helper()
	s := secureHub(t, domains...)
	go s.Serve()
	return s
}

func TestSecureServesTLS(t *testing.T) {
	s := secureFixture(t, "hub.acme.test")

	_, err := tls.Dial("tcp", s.Addr(), &tls.Config{
		ServerName:         "stranger.example",
		InsecureSkipVerify: true,
	})
	if err == nil {
		t.Fatal("the hub offered a certificate for a domain it was not given")
	}
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
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAlsoServeAfterServeIsRefused(t *testing.T) {
	s := secureFixture(t, "hub.acme.test")
	waitFor(t, func() bool {
		_, err := s.AlsoServe("127.0.0.1:0")
		return err != nil
	})
}
