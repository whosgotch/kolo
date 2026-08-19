package hub

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// newOrgFile is an org with nothing in it but a name, which is where an invite
// starts from.
func newOrgFile(t *testing.T) string {
	t.Helper()
	return orgFile(t, `{"org": "acme"}`)
}

func TestClaim(t *testing.T) {
	path := newOrgFile(t)
	_, invite, err := AddInvite(path, "team", time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}

	org, member, token, err := Claim(path, invite, "Dana Scully")
	if err != nil {
		t.Fatal(err)
	}
	if member.ID != "dana-scully" {
		t.Errorf("id = %q, want dana-scully", member.ID)
	}
	if member.Name != "Dana Scully" {
		t.Errorf("name = %q, want Dana Scully", member.Name)
	}

	// The token she was handed is the one she now authenticates with, and it is
	// on disk as a hash rather than as itself.
	if got, ok := org.VerifyMember(token); !ok || got.ID != member.ID {
		t.Error("the token Claim returned does not identify the member it made")
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.VerifyMember(token); !ok {
		t.Error("the new member did not survive being written down")
	}
	if _, ok := reloaded.VerifyMember(invite); ok {
		t.Error("an invite authenticates as a member; it is not supposed to be one")
	}
}

func TestClaimRefusals(t *testing.T) {
	path := newOrgFile(t)
	_, expired, err := AddInvite(path, "old", time.Now().Add(-time.Minute), 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := Claim(path, "kolo_nothing", "Dana"); !errors.Is(err, ErrNoInvite) {
		t.Errorf("unknown invite: err = %v, want ErrNoInvite", err)
	}
	if _, _, _, err := Claim(path, expired, "Dana"); !errors.Is(err, ErrInviteSpent) {
		t.Errorf("expired invite: err = %v, want ErrInviteSpent", err)
	}

	// A refused claim leaves nobody behind.
	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(org.Members) != 0 {
		t.Errorf("a refused claim added %d member(s)", len(org.Members))
	}
}

func TestClaimUsesRunOut(t *testing.T) {
	path := newOrgFile(t)
	_, invite, err := AddInvite(path, "pair", time.Now().Add(time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}

	for i, name := range []string{"Dana", "Fox"} {
		if _, _, _, err := Claim(path, invite, name); err != nil {
			t.Fatalf("claim %d: %v", i+1, err)
		}
	}
	if _, _, _, err := Claim(path, invite, "Walter"); !errors.Is(err, ErrInviteSpent) {
		t.Errorf("third claim on a two-use invite: err = %v, want ErrInviteSpent", err)
	}
}

// Two people opening the same link at once is the ordinary case, not the
// unlikely one: the link went out to everybody in the same message.
func TestClaimTogether(t *testing.T) {
	path := newOrgFile(t)
	_, invite, err := AddInvite(path, "team", time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Serialised, as the hub serialises them. What is under test is that two
	// claims of one invite make two members, each with a working token.
	var mu sync.Mutex
	tokens := make([]string, 4)
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			_, _, token, err := Claim(path, invite, "Dana")
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			tokens[i] = token
		}()
	}
	wg.Wait()

	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(org.Members) != len(tokens) {
		t.Fatalf("%d members, want %d", len(org.Members), len(tokens))
	}
	// Four people who all typed the same name are four members, each reachable
	// by their own token.
	seen := map[string]bool{}
	for _, token := range tokens {
		m, ok := org.VerifyMember(token)
		if !ok {
			t.Fatal("a token handed out does not authenticate")
		}
		if seen[m.ID] {
			t.Errorf("two members share the id %q", m.ID)
		}
		seen[m.ID] = true
	}
}

func TestSlug(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"Dana", "dana"},
		{"Dana Scully", "dana-scully"},
		{"  Dana   Scully  ", "dana-scully"},
		{"O'Neill", "o-neill"},
		{"Ада", "ада"},
		{"!!!", "member"},
		{"", "member"},
	} {
		if got := slug(c.name); got != c.want {
			t.Errorf("slug(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFreeID(t *testing.T) {
	org := &Org{
		Name:    "acme",
		Members: []Member{{ID: "dana"}},
		Hosts:   []Host{{ID: "dana-2"}},
		Invites: []Invite{{ID: "dana-3"}},
	}
	// Members, hosts and invites share a namespace, so the fourth Dana is -4.
	if got := org.freeID("dana"); got != "dana-4" {
		t.Errorf("freeID = %q, want dana-4", got)
	}
	if got := org.freeID("fox"); got != "fox" {
		t.Errorf("freeID = %q, want fox", got)
	}
}

func TestClaimNeedsAFile(t *testing.T) {
	if _, _, _, err := Claim("", "kolo_whatever", "Dana"); err == nil {
		t.Error("claiming against no file should fail rather than lose the member")
	}
}
