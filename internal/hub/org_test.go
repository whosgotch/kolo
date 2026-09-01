package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func orgFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "org.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(Org{
		Name:    "acme",
		Members: []Member{{ID: "artem", Name: "Artem", TokenHash: hash}},
	})

	org, err := Load(orgFile(t, string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if org.Name != "acme" || len(org.Members) != 1 {
		t.Fatalf("loaded %+v", org)
	}
	m, ok := org.VerifyMember(token)
	if !ok || m.ID != "artem" {
		t.Errorf("Verify(the member's own token) = %+v, %v", m, ok)
	}
}

func TestVerifyRejects(t *testing.T) {
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	org := &Org{Name: "acme", Members: []Member{{ID: "artem", TokenHash: hash}}}

	for name, attempt := range map[string]string{
		"empty":               "",
		"another token":       mustToken(t),
		"the stored hash":     hash,
		"prefix only":         TokenPrefix,
		"token with a suffix": token + "x",
		"token truncated":     token[:len(token)-1],
		"uppercased":          strings.ToUpper(token),
	} {
		t.Run(name, func(t *testing.T) {
			if m, ok := org.VerifyMember(attempt); ok {
				t.Errorf("Verify(%s) let in %q", name, m.ID)
			}
		})
	}
}

func TestVerifyIgnoresSurroundingSpace(t *testing.T) {
	token, hash, _ := NewToken()
	org := &Org{Name: "acme", Members: []Member{{ID: "artem", TokenHash: hash}}}

	if _, ok := org.VerifyMember("  " + token + "\n"); !ok {
		t.Error("a token with surrounding whitespace was rejected")
	}
}

func TestRevocation(t *testing.T) {
	token, hash, _ := NewToken()
	org := &Org{Name: "acme", Members: []Member{{ID: "artem", TokenHash: hash}}}
	if _, ok := org.VerifyMember(token); !ok {
		t.Fatal("member cannot connect before revocation")
	}

	org.Members = nil
	if m, ok := org.VerifyMember(token); ok {
		t.Errorf("revoked token still admits %q", m.ID)
	}
}

func TestLoadRejectsBrokenConfigs(t *testing.T) {
	for name, body := range map[string]string{
		"not json":          `{`,
		"no org name":       `{"members":[]}`,
		"member with no id": `{"org":"acme","members":[{"name":"Artem","token_hash":"ab"}]}`,
		"duplicate ids":     `{"org":"acme","members":[{"id":"a","token_hash":"ab"},{"id":"a","token_hash":"cd"}]}`,
		"no token hash":     `{"org":"acme","members":[{"id":"a"}]}`,
		"unreadable hash":   `{"org":"acme","members":[{"id":"a","token_hash":"not-hex"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(orgFile(t, body)); err == nil {
				t.Errorf("Load accepted a config that is %s", name)
			}
		})
	}
}

func TestLoadReportsAMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("Load accepted a path that does not exist")
	}
}

func TestNewTokenIsUnpredictable(t *testing.T) {
	seen := make(map[string]bool, 100)
	for range 100 {
		token, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(token, TokenPrefix) {
			t.Fatalf("token %q has no %q prefix", token, TokenPrefix)
		}
		if seen[token] {
			t.Fatalf("token %q was issued twice", token)
		}
		if strings.Contains(hash, strings.TrimPrefix(token, TokenPrefix)) {
			t.Fatal("the hash contains the token")
		}
		seen[token] = true
	}
}

func mustToken(t *testing.T) string {
	t.Helper()
	token, _, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestTokenKindsDoNotCross(t *testing.T) {
	memberToken, memberHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	hostToken, hostHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	org := &Org{
		Name:    "acme",
		Members: []Member{{ID: "artem", Name: "Artem", TokenHash: memberHash}},
		Hosts:   []Host{{ID: "devbox", TokenHash: hostHash}},
	}

	if h, ok := org.VerifyHost(hostToken); !ok || h.ID != "devbox" {
		t.Errorf("VerifyHost(the host's own token) = %+v, %v", h, ok)
	}
	if m, ok := org.VerifyMember(memberToken); !ok || m.ID != "artem" {
		t.Errorf("VerifyMember(the member's own token) = %+v, %v", m, ok)
	}
	if m, ok := org.VerifyMember(hostToken); ok {
		t.Errorf("a host's token verified as member %q", m.ID)
	}
	if h, ok := org.VerifyHost(memberToken); ok {
		t.Errorf("a member's token verified as host %q", h.ID)
	}
}

func TestAnIdIsUniqueAcrossMembersAndHosts(t *testing.T) {
	_, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(Org{
		Name:    "acme",
		Members: []Member{{ID: "devbox", Name: "Devbox", TokenHash: hash}},
		Hosts:   []Host{{ID: "devbox", TokenHash: hash}},
	})

	if _, err := Load(orgFile(t, string(body))); err == nil {
		t.Error("loaded an org where a member and a host share an id")
	} else if !strings.Contains(err.Error(), "twice") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestAddPutsThemInTheRightList(t *testing.T) {
	path := orgFile(t, `{"org": "acme"}`)

	memberToken, memberHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	hostToken, hostHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddMember(path, Member{ID: "artem", Name: "Artem", TokenHash: memberHash}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddHost(path, Host{ID: "devbox", TokenHash: hostHash}); err != nil {
		t.Fatal(err)
	}

	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := org.VerifyMember(memberToken); !ok || m.Name != "Artem" {
		t.Errorf("the member did not come back: %+v, %v", m, ok)
	}
	if h, ok := org.VerifyHost(hostToken); !ok || h.ID != "devbox" {
		t.Errorf("the machine did not come back: %+v, %v", h, ok)
	}
	if _, ok := org.VerifyMember(hostToken); ok {
		t.Error("the machine was written into the members list")
	}
}

func TestAddKeepsWhoIsAlreadyThere(t *testing.T) {
	path := orgFile(t, `{"org": "acme"}`)
	for _, id := range []string{"artem", "dana", "kirill"} {
		_, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AddMember(path, Member{ID: id, Name: id, TokenHash: hash}); err != nil {
			t.Fatal(err)
		}
	}
	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(org.Members) != 3 || org.Name != "acme" {
		t.Fatalf("adding a third left %+v", org)
	}
}

func TestAddRefusesADuplicate(t *testing.T) {
	path := orgFile(t, `{"org": "acme"}`)
	_, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddMember(path, Member{ID: "artem", Name: "Artem", TokenHash: hash}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddHost(path, Host{ID: "artem", TokenHash: hash}); err == nil {
		t.Fatal("a machine took a name a member already had")
	}
	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(org.Hosts) != 0 {
		t.Errorf("the refused entry was written anyway: %+v", org.Hosts)
	}
}

func TestAddNeedsAnOrgFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org.json")
	_, err := AddMember(path, Member{ID: "artem", Name: "Artem", TokenHash: "ab"})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing org file reported as %v, which callers cannot recognise", err)
	}
}

func TestJoinCarriesTheHubAndTheToken(t *testing.T) {
	token, _, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	joined := NewJoin("https://hub.acme.com", token)
	if !strings.HasPrefix(joined, TokenPrefix) {
		t.Errorf("a join string does not look like a kolo secret: %s", joined)
	}

	gotHub, gotToken, err := ParseJoin(joined)
	if err != nil {
		t.Fatal(err)
	}
	if gotHub != "https://hub.acme.com" || gotToken != token {
		t.Errorf("came back as %q, %q", gotHub, gotToken)
	}
	if _, _, err := ParseJoin("  " + joined + "\n"); err != nil {
		t.Errorf("refused a join string with space around it: %v", err)
	}
}

func TestParseJoinRefusesWhatIsNotOne(t *testing.T) {
	token, _, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, in string }{
		{"nothing", ""},
		{"a plain token, which is half of one", token},
		{"a damaged one", JoinPrefix + "!!!not base64!!!"},
		{"one carrying nothing", JoinPrefix + "e30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ParseJoin(tc.in); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// Removing several at once is one write, so a hub reloading mid-cull never
// sees half of them still in.
func TestRemoveMembersTakesSeveral(t *testing.T) {
	path := newOrgFile(t)
	for _, id := range []string{"dana", "artem", "sam"} {
		_, hash, _ := NewToken()
		if _, err := AddMember(path, Member{ID: id, Name: id, TokenHash: hash}); err != nil {
			t.Fatal(err)
		}
	}
	org, gone, err := RemoveMembers(path, []string{"dana", "sam"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 2 || gone[0] != "dana" || gone[1] != "sam" {
		t.Errorf("removed %v, want dana and sam", gone)
	}
	if len(org.Members) != 1 || org.Members[0].ID != "artem" {
		t.Errorf("left %v, want artem alone", org.Members)
	}
}

// An id that isn't there takes nobody with it: half a cull is worse than
// none, because whoever survived is the part you can't see.
func TestRemoveMembersRefusesAnUnknownId(t *testing.T) {
	path := newOrgFile(t)
	_, hash, _ := NewToken()
	if _, err := AddMember(path, Member{ID: "dana", Name: "Dana", TokenHash: hash}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RemoveMembers(path, []string{"dana", "nope"}); !errors.Is(err, ErrNoSuchMember) {
		t.Fatalf("err = %v, want ErrNoSuchMember", err)
	}
	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := org.Member("dana"); !ok {
		t.Error("the member named alongside a typo was removed anyway")
	}
}

// The point of removing somebody: the token they hold stops working. Read
// back from disk, since that is what a running hub reloads.
func TestRemoveMembersEndsTheirToken(t *testing.T) {
	path := newOrgFile(t)
	token, hash, _ := NewToken()
	if _, err := AddMember(path, Member{ID: "dana", Name: "Dana", TokenHash: hash}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RemoveMembers(path, []string{"dana"}); err != nil {
		t.Fatal(err)
	}
	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := org.VerifyMember(token); ok {
		t.Errorf("a removed member still admits %q", m.ID)
	}
}

// Two kolo processes writing the org file at once: the hub taking a join
// while the operator runs kolo invite in another terminal. Members and
// invites are lists inside one file, so without a lock the writer that
// loaded it first writes the other's work back out of existence.
func TestSeveralWritersKeepEachOthersWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org.json")
	if _, err := Init(path, "acme"); err != nil {
		t.Fatal(err)
	}
	org, token, err := AddInvite(path, "team", time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = org

	const joiners = 10
	var wg sync.WaitGroup
	for i := range joiners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, _, err := Claim(path, token, fmt.Sprintf("person %d", i)); err != nil {
				t.Errorf("claim %d: %v", i, err)
			}
		}()
	}
	// And the operator, minting links beside them.
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := SetInvite(path, fmt.Sprintf("guests-%d", i), time.Now().Add(time.Hour), 5); err != nil {
				t.Errorf("invite %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	final, err := Load(path)
	if err != nil {
		t.Fatalf("the org file is unreadable after concurrent writes: %v", err)
	}
	if len(final.Members) != joiners {
		t.Errorf("%d of %d members were lost, each holding a token they were told was theirs",
			joiners-len(final.Members), joiners)
	}
	if len(final.Invites) != 3 {
		t.Errorf("invites = %d, want the standing one and the two minted beside it", len(final.Invites))
	}
}

// The hub writes down where it is so the commands that print links can read
// it back, and moving the hub says so at its next start.
func TestSetHubURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org.json")
	if _, err := Init(path, "acme"); err != nil {
		t.Fatal(err)
	}
	if org, _ := Load(path); org.Hub != "" {
		t.Errorf("a fresh org already claims a hub at %q", org.Hub)
	}
	if _, err := SetHubURL(path, "http://192.168.0.12:7300"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetHubURL(path, "https://hub.acme.com"); err != nil {
		t.Fatal(err)
	}
	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if org.Hub != "https://hub.acme.com" {
		t.Errorf("org.Hub = %q, want the address of the most recent start", org.Hub)
	}
}
