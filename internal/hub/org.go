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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
	"unicode"
)

// Org is an organisation and the people in it: a file the operator edits by
// hand. Membership is small and changes rarely, so a file that reads at a glance
// and lives in version control beats anywhere to click.
//
// It is read by a hub that may be running and written by one taking a claim, so
// an edit is picked up rather than waited on. See watchOrg.
type Org struct {
	Name    string   `json:"org"`
	Members []Member `json:"members"`
	Hosts   []Host   `json:"hosts"`
	Invites []Invite `json:"invites,omitempty"`

	// Where this was read from, so an org that grows a member while the hub is
	// running can write itself back. Empty for one built in memory, which is
	// what refuses a claim rather than losing it.
	path string
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

// Invite is a link that turns whoever opens it into a member. It exists because
// the alternative is minting one token per person and sending each of them
// somewhere private: an org gets one link, in the channel it already has.
//
// It is weaker than a member's token — it can only be spent on becoming a
// member, and only until it expires — which is what makes it safe to paste
// where a team can see it.
type Invite struct {
	// ID says what this invite was for, in the file and in the log. It is not
	// a secret and is never asked for.
	ID        string `json:"id"`
	TokenHash string `json:"token_hash"`
	// Expires is when it stops working. Always set: a link with no end is one
	// nobody remembers to withdraw.
	Expires time.Time `json:"expires"`
	// Uses left, or zero for as many as the window allows. A team link is
	// bounded by time rather than by a count nobody can predict — the twenty
	// first person to click is not the attacker.
	Uses int `json:"uses,omitempty"`
}

// Spent reports whether an invite can no longer be claimed.
func (i Invite) Spent(now time.Time) bool { return now.After(i.Expires) }

// Person is a member as everyone else sees them. A separate type from the one
// carrying the token hash, so keeping the secret out of a response is a property
// of the design rather than something to remember at each call site.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Person returns the member without their secret.
func (m Member) Person() Person { return Person{ID: m.ID, Name: m.Name} }

// Load reads an org from a JSON file. A running hub reads it again when it
// changes, so revoking a member is removing their line.
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
	org.path = path
	return &org, nil
}

// AddMember records a new person in the org file at path.
func AddMember(path string, m Member) (*Org, error) {
	return update(path, func(o *Org) error { o.Members = append(o.Members, m); return nil })
}

// AddHost records a new machine in the org file at path.
func AddHost(path string, h Host) (*Org, error) {
	return update(path, func(o *Org) error { o.Hosts = append(o.Hosts, h); return nil })
}

// SetHost records a machine, replacing any entry it already has. A host's token
// is not recoverable from the file, so a machine that mints its own on every
// start replaces the hash rather than accumulating one per start.
func SetHost(path string, h Host) (*Org, error) {
	return update(path, func(o *Org) error {
		for i, existing := range o.Hosts {
			if existing.ID == h.ID {
				o.Hosts[i] = h
				return nil
			}
		}
		o.Hosts = append(o.Hosts, h)
		return nil
	})
}

// Init creates an org file holding nothing but a name, and returns false if one
// was already there. The name is the one thing kolo cannot pick, so a caller
// that has no better idea passes something it can explain.
func Init(path, name string) (created bool, err error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("hub: %s: %w", path, err)
	}
	org := &Org{Name: name}
	if err := org.validate(); err != nil {
		return false, fmt.Errorf("hub: %w", err)
	}
	b, _ := json.MarshalIndent(org, "", "  ")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return false, fmt.Errorf("hub: write %s: %w", path, err)
	}
	return true, nil
}

// update applies a change to the org file.
//
// The command that mints a token is the one that knows which list the entry
// belongs in, so it writes it rather than printing it for somebody to paste into
// the right place. A hash in the wrong list is a member who cannot connect, or a
// machine that can, and neither says so at the time.
//
// The file is written whole and renamed into place: it is read by a hub that may
// be running, and a half-written org is one nobody belongs to.
func update(path string, change func(*Org) error) (*Org, error) {
	org, err := Load(path)
	if err != nil {
		return nil, err
	}
	if err := change(org); err != nil {
		return nil, err
	}
	// The same check the hub makes at startup, so a duplicate id is refused here
	// rather than written down and met later as a hub that will not start.
	if err := org.validate(); err != nil {
		return nil, fmt.Errorf("hub: %s: %w", path, err)
	}

	b, err := json.MarshalIndent(org, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("hub: write %s: %w", path, err)
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("hub: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("hub: write %s: %w", path, err)
	}
	return org, nil
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
	for i, v := range o.Invites {
		if err := check("invite", v.ID, v.TokenHash, i); err != nil {
			return err
		}
	}
	return nil
}

// taken reports whether an id belongs to anything already. Members, hosts and
// invites share one namespace, so a name in the log identifies one thing.
func (o *Org) taken(id string) bool {
	for _, m := range o.Members {
		if m.ID == id {
			return true
		}
	}
	for _, h := range o.Hosts {
		if h.ID == id {
			return true
		}
	}
	for _, v := range o.Invites {
		if v.ID == id {
			return true
		}
	}
	return false
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

// knowsMember reports whether a hash is still one a member holds. Not
// constant-time, and not needing to be: the hash comes from a connection this
// hub authenticated earlier, not from whoever is on the other end of it.
func (o *Org) knowsMember(hash string) bool {
	for _, m := range o.Members {
		if m.TokenHash == hash {
			return true
		}
	}
	return false
}

func (o *Org) knowsHost(hash string) bool {
	for _, h := range o.Hosts {
		if h.TokenHash == hash {
			return true
		}
	}
	return false
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

// Path is where this org was read from, or empty for one built in memory.
func (o *Org) Path() string { return o.path }

// Refusals a caller shows to whoever clicked the link. They say the invite is no
// good without saying which of the two it was, because the difference is only
// useful to somebody trying invites.
var (
	ErrNoInvite    = errors.New("hub: this invite is not one this hub knows")
	ErrInviteSpent = errors.New("hub: this invite has run out")
)

// AddInvite records a link that turns whoever opens it into a member, and
// returns the token to put in it.
func AddInvite(path, id string, expires time.Time, uses int) (*Org, string, error) {
	token, hash, err := NewToken()
	if err != nil {
		return nil, "", err
	}
	org, err := update(path, func(o *Org) error {
		o.Invites = append(o.Invites, Invite{ID: o.freeID(id), TokenHash: hash, Expires: expires, Uses: uses})
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return org, token, nil
}

// Claim spends an invite on a new member and returns them with the token they
// will use from now on. The file is re-read first, so an invite withdrawn by
// hand is honoured and a claim lands alongside whatever else has changed.
//
// It is the one thing that adds to an org while the hub is running. Everything
// else is a restart, because everything else is rare.
func Claim(path, token, name string) (org *Org, member Member, memberToken string, err error) {
	memberToken, hash, err := NewToken()
	if err != nil {
		return nil, Member{}, "", err
	}
	org, err = update(path, func(o *Org) error {
		i, ok := o.findInvite(token)
		if !ok {
			return ErrNoInvite
		}
		if o.Invites[i].Spent(time.Now()) {
			return ErrInviteSpent
		}
		if o.Invites[i].Uses > 0 {
			o.Invites[i].Uses--
			// A link with one use left is spent by using it, and an expiry in
			// the past is how every other reader already knows that.
			if o.Invites[i].Uses == 0 {
				o.Invites[i].Expires = time.Now().Add(-time.Second)
			}
		}
		member = Member{ID: o.freeID(slug(name)), Name: name, TokenHash: hash}
		o.Members = append(o.Members, member)
		return nil
	})
	if err != nil {
		return nil, Member{}, "", err
	}
	return org, member, memberToken, nil
}

// findInvite is constant-time per invite, like the other two, so neither the
// duration nor the match can be read from outside.
func (o *Org) findInvite(token string) (int, bool) {
	want := HashToken(token)
	found, ok := 0, false
	for i, v := range o.Invites {
		if subtle.ConstantTimeCompare([]byte(v.TokenHash), []byte(want)) == 1 {
			found, ok = i, true
		}
	}
	return found, ok
}

// freeID is base, or base with a number after it if base is spoken for. Two
// people called Dana are a thing that happens, and the second one joining is not
// an error worth showing her.
func (o *Org) freeID(base string) string {
	if !o.taken(base) {
		return base
	}
	for n := 2; ; n++ {
		id := fmt.Sprintf("%s-%d", base, n)
		if !o.taken(id) {
			return id
		}
	}
}

// slug is a typed name reduced to something that reads well in a log and a URL.
// A name with nothing usable in it — one written in a script this does not know
// — still needs an id, so it gets a plain one rather than an empty one.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		default:
			dash = true
		}
	}
	if b.Len() == 0 {
		return "member"
	}
	return b.String()
}
