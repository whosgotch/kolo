package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/whosgotch/kolo/internal/detect"
)

// fileHub is a hub started from a file on disk, which is what reloading needs
// and what hubFixture, holding its org in memory, deliberately is not.
func fileHub(t *testing.T) (*Server, string, string, string) {
	t.Helper()
	memberToken, memberHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	hostToken, hostHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(Org{
		Name:    "acme",
		Members: []Member{{ID: "artem", Name: "Artem", TokenHash: memberHash}},
		Hosts:   []Host{{ID: "devbox", TokenHash: hostHash}},
	})
	path := orgFile(t, string(body))

	org, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Listen(org, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path, memberToken, hostToken
}

// writeOrg replaces the org file, as an operator editing it does.
func writeOrg(t *testing.T, path string, org Org) {
	t.Helper()
	body, _ := json.MarshalIndent(org, "", "  ")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReloadAddsAndRemoves(t *testing.T) {
	s, path, memberToken, _ := fileHub(t)

	if _, ok := s.verifyMember(memberToken); !ok {
		t.Fatal("the member the hub started with cannot connect")
	}

	// Somebody new, minted by an operator who did not restart anything.
	danaToken, danaHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	writeOrg(t, path, Org{
		Name:    "acme",
		Members: []Member{{ID: "dana", Name: "Dana", TokenHash: danaHash}},
		Hosts:   []Host{{ID: "devbox", TokenHash: HashToken("x")}},
	})
	if _, changed := s.reload(path, nil); !changed {
		t.Fatal("a changed file was not read")
	}

	if _, ok := s.verifyMember(danaToken); !ok {
		t.Error("a member added while the hub ran cannot connect")
	}
	if _, ok := s.verifyMember(memberToken); ok {
		t.Error("a member removed while the hub ran can still connect")
	}
}

// A file that will not parse must not empty the org: nobody could connect, and
// the cause would be a typo somebody made while adding one person.
func TestReloadKeepsWhatWorksOnABadEdit(t *testing.T) {
	s, path, memberToken, _ := fileHub(t)

	if err := os.WriteFile(path, []byte(`{"org": "acme", "members": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, changed := s.reload(path, nil)
	if !changed || raw == nil {
		t.Fatal("a broken file should be remembered, so it is complained about once")
	}
	if _, ok := s.verifyMember(memberToken); !ok {
		t.Error("a typo in the org file locked out a member who was already in it")
	}

	// An org that validates but is empty of people is a deliberate edit, and is
	// applied like any other.
	writeOrg(t, path, Org{Name: "acme"})
	if _, changed := s.reload(path, nil); !changed {
		t.Fatal("the fixed file was not read")
	}
	if _, ok := s.verifyMember(memberToken); ok {
		t.Error("removing every member did not take effect")
	}
}

func TestReloadIgnoresAnUnchangedFile(t *testing.T) {
	s, path, _, _ := fileHub(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed := s.reload(path, raw); changed {
		t.Error("a file nobody touched was treated as an edit")
	}
}

// Revoking a machine has to reach the machine. A host holds one connection and
// makes no further requests, so nothing else would ever notice.
func TestReloadDisconnectsARevokedHost(t *testing.T) {
	s, path, _, hostToken := fileHub(t)
	go s.Serve()
	ctx := testContext(t)

	conn := joinAsHost(t, ctx, s, hostToken)
	if hosts := s.registry.Hosts(); len(hosts) != 1 {
		t.Fatalf("%d hosts connected, want 1", len(hosts))
	}

	writeOrg(t, path, Org{Name: "acme", Members: []Member{{ID: "artem", TokenHash: HashToken("y")}}})
	s.reload(path, nil)

	// Read with no deadline of its own, so what ends it is the connection
	// closing rather than this test giving up — the difference between an
	// assertion and a pause.
	mustClose(t, conn, "a host removed from the org stayed connected")
	waitFor(t, func() bool { return len(s.registry.Hosts()) == 0 })
}

// The same for a browser: a member watching an agent is handed frames and asks
// for nothing, so their removal has to reach the stream.
func TestReloadDisconnectsARevokedWatcher(t *testing.T) {
	s, path, memberToken, hostToken := fileHub(t)
	go s.Serve()
	ctx := testContext(t)

	joinAsHost(t, ctx, s, hostToken)
	s.screens.open("scout", 80, 24, detect.Markers{})
	watch, _, err := websocket.Dial(ctx, "ws://"+s.Addr()+"/v1/watch/scout", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + memberToken}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer watch.CloseNow()

	writeOrg(t, path, Org{Name: "acme", Hosts: []Host{{ID: "devbox", TokenHash: HashToken(hostToken)}}})
	s.reload(path, nil)

	mustClose(t, watch, "a member removed from the org kept watching")
}

// mustClose fails unless the other end hangs up. The read is given no deadline:
// a read that ends because the test ran out of patience looks exactly like one
// that ends because the connection closed, and only one of them is the point.
func mustClose(t *testing.T, conn *websocket.Conn, whenNot string) {
	t.Helper()
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal(whenNot)
	}
}
