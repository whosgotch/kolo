package hub

import (
	"bytes"
	"log"
	"os"
	"time"
)

// How often the org file is read again. A file this small is cheaper to read
// than to watch, and the delay is what somebody who has just edited it waits —
// short enough not to wonder whether it worked.
//
// A variable so the tests do not have to wait it out.
var reloadEvery = 2 * time.Second

// watchOrg applies edits to the org file without a restart.
//
// Adding a member used to mean restarting the hub, which meant disconnecting
// every host to let one person in — so revoking, which is the same edit, was
// either done late or not at all. A security control nobody reaches for is not
// one.
//
// The file is the truth and this hub is one of its readers, even though it is
// also a writer: a claim and a reload never overlap, so neither loses the other.
func (s *Server) watchOrg() {
	path := s.orgPath()
	if path == "" {
		return
	}
	last, err := os.ReadFile(path)
	if err != nil {
		log.Printf("hub: %s cannot be watched for changes: %v", path, err)
		return
	}

	tick := time.NewTicker(reloadEvery)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
			if next, changed := s.reload(path, last); changed {
				last = next
			}
		}
	}
}

// reload re-reads the org file and returns what it found, whether or not it
// could be used. A file that will not parse is still remembered, so a typo is
// complained about once rather than every couple of seconds.
func (s *Server) reload(path string, last []byte) ([]byte, bool) {
	// Held for the whole read-and-swap: a claim writing a new member between the
	// two would be read back over by what was on disk a moment before.
	s.orgFile.Lock()
	defer s.orgFile.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		log.Printf("hub: %s cannot be read: %v; keeping the org already loaded", path, err)
		return nil, false
	}
	if bytes.Equal(raw, last) {
		return nil, false
	}

	org, err := Load(path)
	if err != nil {
		// Kept, rather than applied: an org that will not parse would otherwise
		// mean nobody in it can connect, which is a worse answer to a typo than
		// carrying on with what was already working.
		log.Printf("hub: %v; keeping the org already loaded", err)
		return raw, true
	}

	s.orgMu.Lock()
	s.org = org
	s.orgMu.Unlock()

	// Reported because the difference between an edit that took and one that was
	// mistyped is otherwise invisible from outside.
	log.Printf("hub: %s reloaded: %d member(s), %d host(s)", path, len(org.Members), len(org.Hosts))
	if dropped := s.conns.dropUnknown(org); len(dropped) > 0 {
		log.Printf("hub: disconnected %v, no longer in the org", dropped)
	}
	return raw, true
}
