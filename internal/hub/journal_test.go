package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func journalFixture(t *testing.T) (*journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j, path
}

func dana() Person { return Person{ID: "dana", Name: "Dana"} }

func TestJournalSurvivesRestart(t *testing.T) {
	j, path := journalFixture(t)
	j.add(Entry{Agent: "checkups", What: WhatCreated, Who: dana(), Text: "/work/api — claude"})
	j.add(Entry{Agent: "checkups", What: WhatSaid, Who: dana(), Text: "run the migrations"})
	j.Close()

	again, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	got := again.tail("", 10)
	if len(got) != 2 {
		t.Fatalf("read back %+v", got)
	}
	if got[1].Text != "run the migrations" || got[1].Who != dana() {
		t.Errorf("second entry = %+v", got[1])
	}
	if got[0].At.IsZero() {
		t.Error("an entry with no time on it")
	}
}

// A journal that cannot be opened must not stop the hub, and must still answer
// for the run it is in the middle of.
func TestJournalWithoutAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(filepath.Join(dir, "journal.jsonl"))
	if err == nil && os.Getuid() != 0 {
		t.Fatal("an unreadable journal opened without complaint")
	}
	defer j.Close()

	j.add(Entry{Agent: "checkups", What: WhatStopped, Who: dana()})
	if got := j.tail("", 10); len(got) != 1 {
		t.Fatalf("in memory = %+v", got)
	}
}

func TestJournalTrims(t *testing.T) {
	j, path := journalFixture(t)
	old := time.Now().Add(-2 * keepFor)
	j.now = func() time.Time { return old }
	j.add(Entry{Agent: "checkups", What: WhatSaid, Who: dana(), Text: "last month"})
	j.now = time.Now
	j.add(Entry{Agent: "checkups", What: WhatSaid, Who: dana(), Text: "today"})
	j.Close()

	again, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	got := again.tail("", 10)
	if len(got) != 1 || got[0].Text != "today" {
		t.Fatalf("kept %+v", got)
	}
	// Rewritten, not only filtered on the way in.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "last month") {
		t.Error("the aged out entry is still on disk")
	}
}

// A file a machine died halfway through writing loses its last line, not its
// history.
func TestJournalSkipsAHalfWrittenLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	good := `{"at":"` + time.Now().Format(time.RFC3339) + `","agent":"checkups","what":"said","text":"morning"}`
	if err := os.WriteFile(path, []byte(good+"\n{\"at\":\"2026"), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	if got := j.tail("", 10); len(got) != 1 || got[0].Text != "morning" {
		t.Fatalf("kept %+v", got)
	}
}

func TestJournalTailByAgent(t *testing.T) {
	j, _ := journalFixture(t)
	j.add(Entry{Agent: "checkups", What: WhatSaid, Who: dana(), Text: "one"})
	j.add(Entry{Agent: "docs", What: WhatSaid, Who: dana(), Text: "two"})
	j.add(Entry{Agent: "checkups", What: WhatSaid, Who: dana(), Text: "three"})

	got := j.tail("checkups", 10)
	if len(got) != 2 || got[0].Text != "one" || got[1].Text != "three" {
		t.Fatalf("checkups = %+v", got)
	}
	if got := j.tail("", 2); len(got) != 2 || got[0].Text != "two" {
		t.Fatalf("the last two = %+v", got)
	}
}

func TestTypedLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		want []string
	}{
		{"a line", []string{"hello", "\r"}, []string{"hello"}},
		{"one keystroke at a time", []string{"h", "i", "\r"}, []string{"hi"}},
		{"backspace", []string{"helli", "\x7f", "o", "\r"}, []string{"hello"}},
		{"two lines", []string{"one\rtwo\r"}, []string{"one", "two"}},
		{"arrow keys are not words", []string{"\x1b[Ayes\x1b[B\r"}, []string{"yes"}},
		{"never sent, never recorded", []string{"half a thought"}, nil},
		{"an empty line is not a sentence", []string{"\r\r  \r"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, _ := journalFixture(t)
			for _, k := range tc.keys {
				j.typed("checkups", dana(), k)
			}
			var said []string
			for _, e := range j.tail("checkups", 10) {
				if e.What != WhatSaid {
					t.Fatalf("recorded a %q", e.What)
				}
				said = append(said, e.Text)
			}
			if strings.Join(said, "|") != strings.Join(tc.want, "|") {
				t.Errorf("said %q, want %q", said, tc.want)
			}
		})
	}
}

// Interrupting is somebody deciding not to send what is on the line.
func TestTypedForgotten(t *testing.T) {
	j, _ := journalFixture(t)
	j.typed("checkups", dana(), "half a thought")
	j.forget("checkups")
	j.typed("checkups", dana(), "\r")

	if got := j.tail("checkups", 10); len(got) != 0 {
		t.Fatalf("recorded %+v", got)
	}
}
