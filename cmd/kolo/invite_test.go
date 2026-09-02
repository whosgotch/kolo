package main

import (
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/hub"
)

func someLinks(t *testing.T) *hub.Org {
	t.Helper()
	now := time.Now()
	return &hub.Org{Name: "acme", Invites: []hub.Invite{
		{ID: "team", TokenHash: "ab", Expires: now.Add(time.Hour)},
		{ID: "beta", TokenHash: "cd", Expires: now.Add(-time.Hour)},
		{ID: "all", TokenHash: "ef", Expires: now.Add(-time.Hour)},
	}}
}

// spent is the set worth clearing: the links that no longer work.
func TestResolveSpent(t *testing.T) {
	ids, err := resolve(someLinks(t), []string{"spent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "beta" || ids[1] != "all" {
		t.Errorf("resolve(spent) = %v, want beta and all", ids)
	}
}

// A link actually called all is that link, not every link: its own name wins.
func TestResolvePrefersARealName(t *testing.T) {
	ids, err := resolve(someLinks(t), []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "all" {
		t.Errorf("resolve(all) = %v, want the link named all", ids)
	}
}

func TestResolveEveryLink(t *testing.T) {
	org := someLinks(t)
	org.Invites = org.Invites[:2] // no link called all, so the word stands for the set
	ids, err := resolve(org, []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Errorf("resolve(all) = %v, want both links", ids)
	}
}

func TestResolveNamesTheTypo(t *testing.T) {
	if _, err := resolve(someLinks(t), []string{"tema"}); err == nil {
		t.Error("a name that is not there resolved to something")
	}
}

// The same link asked for twice is withdrawn once.
func TestResolveDeduplicates(t *testing.T) {
	ids, err := resolve(someLinks(t), []string{"team", "team"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Errorf("resolve = %v, want one id", ids)
	}
}

// Where a link says to go. kolo up serves 0.0.0.0 and shows a LAN address,
// so a link that guessed at loopback was one only the lending machine could
// open, which is nobody the link is for.
func TestReachAt(t *testing.T) {
	lan := &hub.Org{Name: "acme", Hub: "http://192.168.0.12:7300"}
	never := &hub.Org{Name: "acme"}

	for _, c := range []struct {
		why   string
		given string
		org   *hub.Org
		want  string
	}{
		{"where the hub said it was", "", lan, "http://192.168.0.12:7300"},
		{"-hub wins over it", "https://hub.acme.com", lan, "https://hub.acme.com"},
		{"a hub that never started", "", never, defaultHubURL},
		{"-hub with nothing written down", "https://hub.acme.com", never, "https://hub.acme.com"},
	} {
		if got := reachAt(c.given, c.org); got != c.want {
			t.Errorf("%s: reachAt(%q) = %q, want %q", c.why, c.given, got, c.want)
		}
	}
}
