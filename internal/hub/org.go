// Package hub is the shared half of kolo: it knows who belongs to an org, which
// machines are lending themselves to it, and what agents are running on them.
//
// Hosts dial in; the hub is never the one to open a connection. What it can ask
// a host to do is bounded by what that host was started with, not by anything
// the hub holds.
package hub

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// Org is an organisation and the people in it: a file the operator edits by
// hand. Membership is small and changes rarely, so a file that reads at a glance
// and lives in version control beats anywhere to click.
type Org struct {
	Name    string   `json:"org"`
	Members []Member `json:"members"`
	Hosts   []Host   `json:"hosts"`
}

// Host is a machine lending itself to the org. It authenticates as itself rather
// than as whoever set it up: "devbox started an agent for dana" says something
// that "artem started an agent" — because the machine was his — does not.
type Host struct {
	ID        string `json:"id"`
	TokenHash string `json:"token_hash"`
}

// Member is one person in an org. Only the hash of their token is stored: this
// file ends up in backups, version control and on screens, each of which would
// otherwise hand out working credentials.
type Member struct {
	// ID is the stable handle used in the protocol and by kolo who.
	ID string `json:"id"`
	// Name is what people are shown.
	Name string `json:"name"`
	// TokenHash is the hex SHA-256 of the member's token.
	TokenHash string `json:"token_hash"`
}

// Person is a member as everyone else sees them. A separate type from the one
// carrying the token hash, so keeping the secret out of a response is a property
// of the design rather than something to remember at each call site.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Person returns the member without their secret.
func (m Member) Person() Person { return Person{ID: m.ID, Name: m.Name} }

// Load reads an org from a JSON file. Changes take effect on restart, so
// revoking a member is removing their line and restarting.
func Load(path string) (*Org, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hub: read org: %w", err)
	}
	var org Org
	if err := json.Unmarshal(b, &org); err != nil {
		return nil, fmt.Errorf("hub: parse %s: %w", path, err)
	}
	if err := org.validate(); err != nil {
		return nil, fmt.Errorf("hub: %s: %w", path, err)
	}
	return &org, nil
}

// validate rejects a config that would behave surprisingly. A duplicate id or an
// unreadable hash is a typo, and saying so at startup beats a member silently
// unable to connect. Ids are unique across members and hosts together, so a name
// in the log identifies one thing.
func (o *Org) validate() error {
	if o.Name == "" {
		return fmt.Errorf("org needs a name")
	}
	seen := map[string]bool{}
	check := func(kind, id, hash string, i int) error {
		switch {
		case id == "":
			return fmt.Errorf("%s %d has no id", kind, i)
		case seen[id]:
			return fmt.Errorf("id %q appears twice", id)
		case hash == "":
			return fmt.Errorf("%s %q has no token hash", kind, id)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return fmt.Errorf("%s %q has an unreadable token hash", kind, id)
		}
		seen[id] = true
		return nil
	}
	for i, m := range o.Members {
		if err := check("member", m.ID, m.TokenHash, i); err != nil {
			return err
		}
	}
	for i, h := range o.Hosts {
		if err := check("host", h.ID, h.TokenHash, i); err != nil {
			return err
		}
	}
	return nil
}

// VerifyMember returns the member a token belongs to. Every member is compared
// against, each comparison constant-time, so neither the duration nor the match
// can be read from outside.
func (o *Org) VerifyMember(token string) (Member, bool) {
	want := HashToken(token)
	var found Member
	var ok bool
	for _, m := range o.Members {
		if subtle.ConstantTimeCompare([]byte(m.TokenHash), []byte(want)) == 1 {
			found, ok = m, true
		}
	}
	return found, ok
}

// VerifyHost returns the host a token belongs to. A member's token is not a
// host's and never resolves here: the two are asked for on different routes and
// carry different powers.
func (o *Org) VerifyHost(token string) (Host, bool) {
	want := HashToken(token)
	var found Host
	var ok bool
	for _, h := range o.Hosts {
		if subtle.ConstantTimeCompare([]byte(h.TokenHash), []byte(want)) == 1 {
			found, ok = h, true
		}
	}
	return found, ok
}
