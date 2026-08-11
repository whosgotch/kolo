package hub

import (
	"encoding/json"
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
	m, ok := org.Verify(token)
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
			if m, ok := org.Verify(attempt); ok {
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

	if _, ok := org.Verify("  " + token + "\n"); !ok {
		t.Error("a token with surrounding whitespace was rejected")
	}
}

// TestRevocation pins what removing a member from the file does.
func TestRevocation(t *testing.T) {
	token, hash, _ := NewToken()
	org := &Org{Name: "acme", Members: []Member{{ID: "artem", TokenHash: hash}}}
	if _, ok := org.Verify(token); !ok {
		t.Fatal("member cannot connect before revocation")
	}

	org.Members = nil
	if m, ok := org.Verify(token); ok {
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
