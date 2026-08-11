package hub

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func member(id string) Member { return Member{ID: id, Name: strings.ToTitle(id)} }

func TestJoinAndLeave(t *testing.T) {
	p := NewPresence()
	if p.Len() != 0 {
		t.Fatalf("a new Presence has %d connections", p.Len())
	}

	c := p.Join(member("artem"), "claude", "artem-mbp")
	if p.Len() != 1 {
		t.Fatalf("Len() = %d after one join", p.Len())
	}
	got := p.List()[0]
	if got.Member.ID != "artem" || got.Agent != "claude" || got.Machine != "artem-mbp" {
		t.Errorf("listed %+v", got)
	}
	if got.Since.IsZero() {
		t.Error("connection has no start time")
	}

	p.Leave(c.ID)
	if p.Len() != 0 {
		t.Errorf("Len() = %d after leaving", p.Len())
	}
}

// TestOneMemberInTwoPlaces is why presence is keyed by connection. A person on a
// laptop and a desktop is two entries, and closing one must not close the other.
func TestOneMemberInTwoPlaces(t *testing.T) {
	p := NewPresence()
	laptop := p.Join(member("artem"), "claude", "laptop")
	desktop := p.Join(member("artem"), "claude", "desktop")

	if p.Len() != 2 {
		t.Fatalf("Len() = %d, want both machines listed", p.Len())
	}
	if laptop.ID == desktop.ID {
		t.Fatal("both connections were given the same id")
	}

	p.Leave(laptop.ID)
	rest := p.List()
	if len(rest) != 1 || rest[0].Machine != "desktop" {
		t.Errorf("after the laptop left: %+v", rest)
	}
}

// TestLeaveIsForgiving covers the paths that clean up a connection: more than
// one of them may run, and they should not have to agree on which got there
// first.
func TestLeaveIsForgiving(t *testing.T) {
	p := NewPresence()
	c := p.Join(member("artem"), "claude", "laptop")

	p.Leave(c.ID)
	p.Leave(c.ID)
	p.Leave(99999)

	if p.Len() != 0 {
		t.Errorf("Len() = %d", p.Len())
	}
}

// TestListIsStable keeps kolo who comparable between runs.
func TestListIsStable(t *testing.T) {
	p := NewPresence()
	at := time.Unix(0, 0)
	p.now = func() time.Time { at = at.Add(time.Minute); return at }

	p.Join(member("dana"), "claude", "dana-pc")
	p.Join(member("artem"), "claude", "second")
	p.Join(member("artem"), "claude", "first")

	var order []string
	for _, c := range p.List() {
		order = append(order, c.Member.ID+"/"+c.Machine)
	}
	want := []string{"artem/second", "artem/first", "dana/dana-pc"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// TestMemberComesFromTheToken pins that a client cannot name itself: the hub
// stores the member it decided on, whatever the client sent.
func TestMemberComesFromTheToken(t *testing.T) {
	p := NewPresence()
	c := p.Join(Member{ID: "artem", Name: "Artem"}, "claude", "artem-mbp")
	if c.Member.ID != "artem" {
		t.Errorf("member = %q", c.Member.ID)
	}
}

// TestLabelsAreSafeToPrint is the reason label exists. kolo who prints these
// into someone else's terminal, and a member being authenticated does not make
// their machine name trustworthy.
func TestLabelsAreSafeToPrint(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "artem-mbp", "artem-mbp"},
		{"escape sequence", "\x1b[2Jwiped", "[2Jwiped"},
		{"carriage return", "real\rfake", "real fake"},
		{"bell", "noisy\x07", "noisy"},
		{"bidi override", "safe‮gnorw", "safegnorw"},
		{"empty", "", "unknown"},
		{"only controls", "\x1b\x07\x00", "unknown"},
		{"collapses spacing", "  two   words ", "two words"},
		{"keeps unicode", "Артём-ноутбук", "Артём-ноутбук"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := label(tt.input); got != tt.want {
				t.Errorf("label(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLabelsAreBounded(t *testing.T) {
	got := label(strings.Repeat("é", maxLabel))
	if len(got) > maxLabel {
		t.Errorf("label is %d bytes, want at most %d", len(got), maxLabel)
	}
	if strings.ContainsRune(got, '�') {
		t.Error("label was cut in the middle of a rune")
	}
}

func TestPresenceIsConcurrentlyUsable(t *testing.T) {
	p := NewPresence()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := p.Join(member("artem"), "claude", "machine")
			p.List()
			p.Len()
			if i%2 == 0 {
				p.Leave(c.ID)
			}
		}()
	}
	wg.Wait()

	if got := p.Len(); got != 25 {
		t.Errorf("Len() = %d, want 25 left connected", got)
	}
}
