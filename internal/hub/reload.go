package hub

import (
	"bytes"
	"log"
	"os"
	"time"
)

// reloadEvery is how often the org file is re-read; a var so tests can shorten it.
var reloadEvery = 2 * time.Second

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

// reload re-reads the org file, returning contents that fail to load too, so
// a broken file is logged once instead of every tick.
func (s *Server) reload(path string, last []byte) ([]byte, bool) {
	// Held across read-and-swap so a concurrent claim cannot interleave.
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
		// Remembered but not applied: an unparsable org would lock everyone out.
		log.Printf("hub: %v; keeping the org already loaded", err)
		return raw, true
	}

	s.orgMu.Lock()
	s.org = org
	s.orgMu.Unlock()

	log.Printf("hub: %s reloaded: %d member(s), %d host(s)", path, len(org.Members), len(org.Hosts))
	if dropped := s.conns.dropUnknown(org); len(dropped) > 0 {
		log.Printf("hub: disconnected %v, no longer in the org", dropped)
	}
	return raw, true
}
