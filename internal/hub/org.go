// Package hub is the shared half of kolo: it knows who belongs to an org and
// which of them are connected right now.
//
// Members' agents run on their own machines and dial in. The hub never reaches
// back into a machine, and holds nothing that lets it: no files, no commands,
// no way to type into anyone's agent.
package hub

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// Org is an organisation and the people in it.
//
// It is a file the operator edits by hand. Membership is small and changes
// rarely, and a file that can be read at a glance and kept in version control is
// worth more at this size than anywhere to click.
type Org struct {
	Name    string   `json:"org"`
	Members []Member `json:"members"`
}

// Member is one person in an org.
//
// Only the hash of their token is stored. A hub's config file ends up in
// backups, in version control and on screens; storing the token itself would
// mean each of those hands out working credentials.
type Member struct {
	// ID is the stable handle used in the protocol and by kolo who.
	ID string `json:"id"`
	// Name is what people are shown.
	Name string `json:"name"`
	// TokenHash is the hex SHA-256 of the member's token.
	TokenHash string `json:"token_hash"`
}

// Person is a member as everyone else sees them. It exists so that the type
// carrying a token hash and the type sent over the wire are not the same type:
// keeping the secret out of a response is then a property of the design rather
// than something to remember at each call site.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Person returns the member without their secret.
func (m Member) Person() Person { return Person{ID: m.ID, Name: m.Name} }

// Load reads an org from a JSON file.
//
// Changes take effect when the hub restarts. Revoking a member is removing
// their line and restarting, which is a blunt instrument and an honest one at
// this size.
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

// validate rejects a config that would behave surprisingly rather than loading
// it and hoping. A duplicate id or an unreadable hash is a mistake in a file
// someone typed, and saying so at startup beats a member silently unable to
// connect.
func (o *Org) validate() error {
	if o.Name == "" {
		return fmt.Errorf("org needs a name")
	}
	seen := make(map[string]bool, len(o.Members))
	for i, m := range o.Members {
		switch {
		case m.ID == "":
			return fmt.Errorf("member %d has no id", i)
		case seen[m.ID]:
			return fmt.Errorf("member id %q appears twice", m.ID)
		case m.TokenHash == "":
			return fmt.Errorf("member %q has no token hash", m.ID)
		}
		if _, err := hex.DecodeString(m.TokenHash); err != nil {
			return fmt.Errorf("member %q has an unreadable token hash", m.ID)
		}
		seen[m.ID] = true
	}
	return nil
}

// Verify returns the member a token belongs to.
//
// Every member is compared against, and each comparison is constant-time, so
// neither how long the call takes nor which member matched can be read from the
// outside.
func (o *Org) Verify(token string) (Member, bool) {
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

// Member returns a member by id.
func (o *Org) Member(id string) (Member, bool) {
	for _, m := range o.Members {
		if m.ID == id {
			return m, true
		}
	}
	return Member{}, false
}
