package hub

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// orgFile writes a config and returns its path.
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

// TestVerifyRejects is the important one: everything that is not a member's
// token has to come back as nobody.
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

// TestVerifyIgnoresSurroundingSpace covers a token pasted out of a chat message
// or a file, which is how they actually travel.
func TestVerifyIgnoresSurroundingSpace(t *testing.T) {
	token, hash, _ := NewToken()
	org := &Org{Name: "acme", Members: []Member{{ID: "artem", TokenHash: hash}}}

	if _, ok := org.VerifyMember("  " + token + "\n"); !ok {
		t.Error("a token with surrounding whitespace was rejected")
	}
}

// TestRevocation pins what removing a member from the file does.
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

// TestNewTokenIsUnpredictable is a smoke test, not a measure of randomness: it
// would catch a token generated from something fixed, which is the mistake worth
// catching.
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

// TestTokenKindsDoNotCross is the point of having two lists. A host's token
// carries the power to run processes on a machine and a member's does not, so
// one resolving as the other would be an escalation in either direction.
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

// TestAddPutsThemInTheRightList is the point of writing the org file rather than
// printing an entry to paste into it: nobody has to decide which list a hash
// belongs in, and a person and a machine do not end up in each other's.
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

	// Read back from disk, because what the hub will do with it is read the file.
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

// TestAddRefusesADuplicate: the hub would refuse this org at startup, so it is
// refused now, while there is somebody to tell.
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

	// The refusal must not have written anything: an org file left holding the
	// entry it just refused is worse than the entry never arriving.
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
	// Pasting is how it travels, and a paste picks up whitespace.
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
