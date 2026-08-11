package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/whosgotch/kolo/internal/term"
)

func TestSubscribeSendsSizeThenSnapshot(t *testing.T) {
	h := NewHub(80, 24)
	h.Write([]byte("hello viewer"))

	backlog, _, cancel := h.Subscribe()
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
	h := NewHub(80, 24)
	_, stream, cancel := h.Subscribe()
	defer cancel()

	h.Write([]byte("live output"))
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
	h := NewHub(80, 24)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 400 {
			h.Write(fmt.Appendf(nil, "\x1b[%d;1H\x1b[3%dmline %03d\x1b[0m", i%24+1, i%8, i))
			if i%50 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	}()

	// Join mid-flight, which is the case that matters.
	time.Sleep(2 * time.Millisecond)
	backlog, stream, cancel := h.Subscribe()

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

	if got, want := viewer.Snapshot(), h.screen.Snapshot(); !bytes.Equal(got, want) {
		t.Errorf("viewer screen diverged from the host's\n got: %q\nwant: %q", got, want)
	}
}

func TestSecondViewerTakesOver(t *testing.T) {
	h := NewHub(80, 24)
	_, first, cancelFirst := h.Subscribe()
	defer cancelFirst()

	_, second, cancelSecond := h.Subscribe()
	defer cancelSecond()

	if _, open := <-first; open {
		t.Error("first viewer still receiving after being replaced")
	}
	h.Write([]byte("x"))
	select {
	case m := <-second:
		if string(m.Data) != "x" {
			t.Errorf("second viewer received %q", m.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("second viewer received nothing")
	}
}

// TestSlowViewerIsDropped pins the overflow policy: the viewer is disconnected,
// not served a stream with a hole in it, and the agent is never held up.
func TestSlowViewerIsDropped(t *testing.T) {
	h := NewHub(80, 24)
	_, stream, cancel := h.Subscribe()
	defer cancel()

	for range viewerBuffer + 10 {
		h.Write([]byte("x"))
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
	h := NewHub(80, 24)
	_, stream, cancel := h.Subscribe()
	defer cancel()

	h.Resize(100, 30)
	select {
	case m := <-stream:
		if !m.Control || !bytes.Contains(m.Data, []byte(`"cols":100`)) {
			t.Errorf("resize sent %q, want a size control frame", m.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("resize was not announced")
	}
	if cols, rows := h.screen.Size(); cols != 100 || rows != 30 {
		t.Errorf("screen is %dx%d, want 100x30", cols, rows)
	}
}
