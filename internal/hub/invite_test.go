package hub

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// newOrgFile is an org with nothing in it but a name, where an invite starts
// from.
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

	// The token she was handed is what she now authenticates with; on disk it
	// sits as a hash rather than as itself.
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

// Two people opening the same link at once is the ordinary case: the link went
// out to everybody in one message.
func TestClaimTogether(t *testing.T) {
	path := newOrgFile(t)
	_, invite, err := AddInvite(path, "team", time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Serialised, as the hub serialises them: two claims of one invite are
	// meant to make two members, each with a working token.
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

func TestWithdrawInvite(t *testing.T) {
	path := newOrgFile(t)
	_, invite, err := AddInvite(path, "team", time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Claim(path, invite, "Dana"); err != nil {
		t.Fatal(err)
	}

	if _, err := WithdrawInvite(path, "team"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Claim(path, invite, "Fox"); !errors.Is(err, ErrNoInvite) {
		t.Errorf("claiming a withdrawn invite: err = %v, want ErrNoInvite", err)
	}

	// Withdrawing a link does not remove whoever came through it; removing a
	// member is its own deliberate act.
	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(org.Members) != 1 || org.Members[0].ID != "dana" {
		t.Errorf("members = %+v, want dana alone", org.Members)
	}

	if _, err := WithdrawInvite(path, "team"); !errors.Is(err, ErrNoSuchInvite) {
		t.Errorf("withdrawing twice: err = %v, want ErrNoSuchInvite", err)
	}
}

// Who came in through which link is the question a leaked invite raises, so a
// member carries the answer.
func TestClaimRecordsWhereTheyCameFrom(t *testing.T) {
	path := newOrgFile(t)
	_, invite, err := AddInvite(path, "contractors", time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	_, member, _, err := Claim(path, invite, "Dana")
	if err != nil {
		t.Fatal(err)
	}
	if member.Via != "contractors" {
		t.Errorf("via = %q, want contractors", member.Via)
	}
	if member.Joined.IsZero() {
		t.Error("a member who joined has no time of joining")
	}
}

func TestLiveInvites(t *testing.T) {
	now := time.Now()
	org := &Org{Name: "acme", Invites: []Invite{
		{ID: "old", Expires: now.Add(-time.Hour)},
		{ID: "team", Expires: now.Add(time.Hour)},
	}}
	live := org.Live(now)
	if len(live) != 1 || live[0].ID != "team" {
		t.Errorf("live = %+v, want team alone", live)
	}
}

// SetInvite is what keeps an org to one link: the same name, minted again,
// replaces what was there rather than becoming team-2.
func TestSetInviteReplacesByName(t *testing.T) {
	path := newOrgFile(t)
	_, first, err := SetInvite(path, "team", time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	org, second, err := SetInvite(path, "team", time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("the replacement carries the same token as the invite it replaced")
	}
	if len(org.Invites) != 1 {
		t.Fatalf("invites = %d, want 1: replacing a link should not add one", len(org.Invites))
	}

	// The old link is gone rather than merely unnamed.
	if _, _, _, err := Claim(path, first, "Dana"); !errors.Is(err, ErrNoInvite) {
		t.Errorf("claiming the replaced link: %v, want ErrNoInvite", err)
	}
	if _, _, _, err := Claim(path, second, "Dana"); err != nil {
		t.Errorf("claiming the replacement: %v", err)
	}
}

// The link kolo shows twice is the one it kept, so a reload still has it.
func TestInviteTokenSurvivesReload(t *testing.T) {
	path := newOrgFile(t)
	_, token, err := SetInvite(path, "team", time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := org.Invite("team")
	if !ok {
		t.Fatal("no invite named team after reload")
	}
	if v.Token != token {
		t.Errorf("kept token = %q, want the one SetInvite returned", v.Token)
	}
	if !v.Showable(time.Now()) {
		t.Error("a live invite whose token was kept should be showable")
	}
}

// An invite from before kolo kept tokens still works; it just can't be shown.
func TestOlderInviteIsNotShowable(t *testing.T) {
	org := &Org{Invites: []Invite{{ID: "team", Expires: time.Now().Add(time.Hour)}}}
	v, _ := org.Invite("team")
	if v.Showable(time.Now()) {
		t.Error("an invite with no kept token should not be showable")
	}
}

// A name already spoken for is a conflict, not a silent overwrite.
func TestSetInviteRefusesAName(t *testing.T) {
	path := orgFile(t, `{"org": "acme", "members": [{"id": "dana", "name": "Dana", "token_hash": "ab"}]}`)
	if _, _, err := SetInvite(path, "dana", time.Now().Add(time.Hour), 10); err == nil {
		t.Error("SetInvite took a name a member already holds")
	}
}

// Withdrawing several at once is one write, so a hub reloading mid-cull
// never sees half of them gone.
func TestWithdrawInvitesTakesSeveral(t *testing.T) {
	path := newOrgFile(t)
	for _, id := range []string{"team", "beta", "contractors"} {
		if _, _, err := SetInvite(path, id, time.Now().Add(time.Hour), 10); err != nil {
			t.Fatal(err)
		}
	}
	org, gone, err := WithdrawInvites(path, []string{"beta", "contractors"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 2 || gone[0] != "beta" || gone[1] != "contractors" {
		t.Errorf("withdrew %v, want beta and contractors", gone)
	}
	if len(org.Invites) != 1 || org.Invites[0].ID != "team" {
		t.Errorf("left %v, want team alone", org.Invites)
	}
}

// A name that isn't there takes nothing with it: half a cull is worse than
// none, because what survived is the part you can't see.
func TestWithdrawInvitesRefusesAnUnknownName(t *testing.T) {
	path := newOrgFile(t)
	if _, _, err := SetInvite(path, "team", time.Now().Add(time.Hour), 10); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WithdrawInvites(path, []string{"team", "nope"}); !errors.Is(err, ErrNoSuchInvite) {
		t.Fatalf("err = %v, want ErrNoSuchInvite", err)
	}
	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := org.Invite("team"); !ok {
		t.Error("the link named alongside a typo was withdrawn anyway")
	}
}
