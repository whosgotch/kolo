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
