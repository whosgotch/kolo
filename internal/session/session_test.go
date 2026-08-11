package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/term"
)

func TestSubscribeSendsSizeThenSnapshot(t *testing.T) {
	s := New(80, 24)
	s.Write([]byte("hello viewer"))

	backlog, _, cancel := s.Subscribe()
	defer cancel()

	if len(backlog) != 2 {
		t.Fatalf("backlog has %d messages, want size then snapshot", len(backlog))
	}
	if !backlog[0].Control {
		t.Error("first message is not a control frame")
	}
	var size struct {
		Type       string
		Cols, Rows int
	}
	if err := json.Unmarshal(backlog[0].Data, &size); err != nil {
		t.Fatalf("control frame: %v", err)
	}
	if size.Type != "size" || size.Cols != 80 || size.Rows != 24 {
		t.Errorf("control frame = %+v, want size 80x24", size)
	}
	if backlog[1].Control {
		t.Error("snapshot was sent as a control frame")
	}
	if !bytes.Contains(backlog[1].Data, []byte("hello viewer")) {
		t.Error("snapshot does not carry what was already on screen")
	}
}

func TestWriteReachesTheViewer(t *testing.T) {
	s := New(80, 24)
	_, stream, cancel := s.Subscribe()
	defer cancel()

	s.Write([]byte("live output"))
	select {
	case m := <-stream:
		if string(m.Data) != "live output" {
			t.Errorf("streamed %q, want %q", m.Data, "live output")
		}
	case <-time.After(time.Second):
		t.Fatal("nothing streamed")
	}
}

// TestSubscribeLosesNothingUnderWrites is the reason Subscribe holds the same
// lock as Write. A viewer that misses a chunk, or receives one already folded
// into its snapshot, renders a corrupted screen from then on and never
// recovers, so the catch-up and the live stream have to join up exactly.
//
// Run with -race for the other half of the guarantee.
func TestSubscribeLosesNothingUnderWrites(t *testing.T) {
	s := New(80, 24)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 400 {
			s.Write(fmt.Appendf(nil, "\x1b[%d;1H\x1b[3%dmline %03d\x1b[0m", i%24+1, i%8, i))
			if i%50 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	}()

	// Join mid-flight, which is the case that matters.
	time.Sleep(2 * time.Millisecond)
	backlog, stream, cancel := s.Subscribe()

	viewer := term.New(80, 24)
	for _, m := range backlog {
		if !m.Control {
			viewer.Write(m.Data)
		}
	}

	// Drain as a real viewer does, concurrently: a viewer that stops reading
	// gets dropped rather than buffered, which is a different test.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for m := range stream {
			if !m.Control {
				viewer.Write(m.Data)
			}
		}
	}()

	<-done
	cancel()
	<-drained

	if got, want := viewer.Snapshot(), s.screen.Snapshot(); !bytes.Equal(got, want) {
		t.Errorf("viewer screen diverged from the host's\n got: %q\nwant: %q", got, want)
	}
}

// TestViewersWatchTogether is the point of the whole thing: guests watch at the
// same time. A second viewer joining must not disturb the first, which an
// earlier take-over rule got wrong — with pages that reconnect on their own, it
// left two viewers knocking each other offline forever.
func TestViewersWatchTogether(t *testing.T) {
	s := New(80, 24)
	_, first, cancelFirst := s.Subscribe()
	defer cancelFirst()

	_, second, cancelSecond := s.Subscribe()
	defer cancelSecond()

	if got := s.Viewers(); got != 2 {
		t.Fatalf("Viewers() = %d, want 2", got)
	}

	s.Write([]byte("x"))
	for name, stream := range map[string]<-chan Message{"first": first, "second": second} {
		select {
		case m := <-stream:
			if string(m.Data) != "x" {
				t.Errorf("%s viewer received %q, want %q", name, m.Data, "x")
			}
		case <-time.After(time.Second):
			t.Errorf("%s viewer received nothing", name)
		}
	}
}

// TestOneViewerLeavingLeavesTheRest covers the other half: a viewer dropped for
// falling behind must not take the others with it.
func TestOneViewerLeavingLeavesTheRest(t *testing.T) {
	s := New(80, 24)
	_, slow, cancelSlow := s.Subscribe()
	defer cancelSlow()
	_, keen, cancelKeen := s.Subscribe()
	defer cancelKeen()

	// Overflow the slow viewer without ever reading from it.
	for range viewerBuffer + 10 {
		s.Write([]byte("x"))
		select {
		case <-keen:
		default:
		}
	}
	if s.Viewers() != 1 {
		t.Errorf("Viewers() = %d, want the slow one dropped and the other kept", s.Viewers())
	}

	drain(slow)
	if _, open := <-slow; open {
		t.Error("slow viewer was not dropped")
	}

	drain(keen)
	s.Write([]byte("later"))
	select {
	case m, open := <-keen:
		if !open {
			t.Error("the viewer that kept up was dropped too")
		} else if string(m.Data) != "later" {
			t.Errorf("the viewer that kept up received %q, want %q", m.Data, "later")
		}
	case <-time.After(time.Second):
		t.Error("the viewer that kept up stopped receiving")
	}
}

// drain empties whatever is buffered without blocking.
func drain(ch <-chan Message) {
	for {
		select {
		case _, open := <-ch:
			if !open {
				return
			}
		default:
			return
		}
	}
}

// TestSlowViewerIsDropped pins the overflow policy: the viewer is disconnected,
// not served a stream with a hole in it, and the agent is never held up.
func TestSlowViewerIsDropped(t *testing.T) {
	s := New(80, 24)
	_, stream, cancel := s.Subscribe()
	defer cancel()

	for range viewerBuffer + 10 {
		s.Write([]byte("x"))
	}

	for range viewerBuffer {
		if _, open := <-stream; !open {
			return // closed, as it should be
		}
	}
	if _, open := <-stream; open {
		t.Error("slow viewer was not dropped")
	}
}

func TestResizeTellsTheViewer(t *testing.T) {
	s := New(80, 24)
	_, stream, cancel := s.Subscribe()
	defer cancel()

	s.Resize(100, 30)
	select {
	case m := <-stream:
		if !m.Control || !bytes.Contains(m.Data, []byte(`"cols":100`)) {
			t.Errorf("resize sent %q, want a size control frame", m.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("resize was not announced")
	}
	if cols, rows := s.screen.Size(); cols != 100 || rows != 30 {
		t.Errorf("screen is %dx%d, want 100x30", cols, rows)
	}
}
