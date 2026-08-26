// Package hub is the shared half of kolo: it knows who belongs to an org, which
// machines are lending themselves to it, and what agents are running on them.
// Hosts dial in; the hub never opens a connection itself.
package hub

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Org is an organisation and the people and machines in it, held in a file
// the operator edits by hand; a running hub picks up edits as they land.
type Org struct {
	Name    string   `json:"org"`
	Members []Member `json:"members"`
	Hosts   []Host   `json:"hosts"`
	Invites []Invite `json:"invites,omitempty"`

	// Where this was loaded from; empty for an org built in memory.
	path string
}

// Host is a machine lending itself to the org, authenticating as itself.
type Host struct {
	ID        string `json:"id"`
	TokenHash string `json:"token_hash"`
}

// Member is one person in an org. Only the token hash is stored: the file
// ends up in backups and version control.
type Member struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TokenHash string `json:"token_hash"`
	// Set only for members who arrived through an invite.
	Joined time.Time `json:"joined,omitempty"`
	Via    string    `json:"via,omitempty"`
}

// Invite is a join link, weaker than a member token: it only mints a member,
// and stops working at Expires.
type Invite struct {
	ID        string `json:"id"`
	TokenHash string `json:"token_hash"`
	// The link's own token, kept where a member's never is. An invite
	// only mints a member and expires on its own, and anyone who can read
	// this file can already write themselves into it — so storing it costs
	// nothing and means the one link can be shown again instead of a new
	// one being minted every time somebody asks for it. Empty for invites
	// written before kolo kept them.
	Token string `json:"token,omitempty"`
	// When it stops working; always set.
	Expires time.Time `json:"expires"`
	// Uses left, or zero for unlimited within the window.
	Uses int `json:"uses,omitempty"`
}

// Showable is an invite whose link can be printed again.
func (i Invite) Showable(now time.Time) bool { return i.Token != "" && !i.Spent(now) }

func (i Invite) Spent(now time.Time) bool { return now.After(i.Expires) }

// Person is a member as others see them, a separate type from Member so the
// token hash can't leak into a response.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (m Member) Person() Person { return Person{ID: m.ID, Name: m.Name} }

// Load reads an org file; a running hub reloads it on change.
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

func AddMember(path string, m Member) (*Org, error) {
	return update(path, func(o *Org) error { o.Members = append(o.Members, m); return nil })
}

func AddHost(path string, h Host) (*Org, error) {
	return update(path, func(o *Org) error { o.Hosts = append(o.Hosts, h); return nil })
}

// SetHost records a host, replacing any existing entry: hosts remint their
// token each start.
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

// Init creates an org file holding only a name; created is false if one existed.
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("hub: %w", err)
	}
	b, _ := json.MarshalIndent(org, "", "  ")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return false, fmt.Errorf("hub: write %s: %w", path, err)
	}
	return true, nil
}

// update rewrites the org file atomically: written whole and renamed into
// place, since a concurrently reloading hub must never see a partial file.
func update(path string, change func(*Org) error) (*Org, error) {
	org, err := Load(path)
	if err != nil {
		return nil, err
	}
	if err := change(org); err != nil {
		return nil, err
	}
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

// validate rejects bad configs; ids share one namespace across members,
// hosts and invites.
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

// VerifyMember returns the member for token, comparing constant-time.
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

// knowsMember needn't be constant-time: the hash comes from an already
// authenticated connection.
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

// VerifyHost returns the host for token; member tokens never resolve here.
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

func (o *Org) Path() string { return o.path }

// Deliberately hard to tell apart: the distinction only helps invite guessing.
var (
	ErrNoInvite    = errors.New("hub: this invite is not one this hub knows")
	ErrInviteSpent = errors.New("hub: this invite has run out")
)

// AddInvite records an invite and returns the token for its link.
func AddInvite(path, id string, expires time.Time, uses int) (*Org, string, error) {
	token, hash, err := NewToken()
	if err != nil {
		return nil, "", err
	}
	org, err := update(path, func(o *Org) error {
		o.Invites = append(o.Invites, Invite{
			ID: o.freeID(id), TokenHash: hash, Token: token, Expires: expires, Uses: uses,
		})
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return org, token, nil
}

// SetInvite mints an invite under exactly this id, replacing any invite that
// already holds it. Where AddInvite would make team-2, this keeps one link
// named team and lets the old one stop working.
func SetInvite(path, id string, expires time.Time, uses int) (*Org, string, error) {
	token, hash, err := NewToken()
	if err != nil {
		return nil, "", err
	}
	fresh := Invite{ID: id, TokenHash: hash, Token: token, Expires: expires, Uses: uses}
	org, err := update(path, func(o *Org) error {
		for i, v := range o.Invites {
			if v.ID == id {
				o.Invites[i] = fresh
				return nil
			}
		}
		if o.taken(id) {
			return fmt.Errorf("hub: %q is a member or host here, so an invite cannot have that name", id)
		}
		o.Invites = append(o.Invites, fresh)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return org, token, nil
}

// Invite returns the invite with this id, spent or not.
func (o *Org) Invite(id string) (Invite, bool) {
	for _, v := range o.Invites {
		if v.ID == id {
			return v, true
		}
	}
	return Invite{}, false
}

// Claim spends an invite on a new member and returns them with a fresh token.
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
			// Spent is marked by backdating expiry, so all readers treat it as expired.
			if o.Invites[i].Uses == 0 {
				o.Invites[i].Expires = time.Now().Add(-time.Second)
			}
		}
		member = Member{
			ID: o.freeID(slug(name)), Name: name, TokenHash: hash,
			Joined: time.Now().Round(time.Second), Via: o.Invites[i].ID,
		}
		o.Members = append(o.Members, member)
		return nil
	})
	if err != nil {
		return nil, Member{}, "", err
	}
	return org, member, memberToken, nil
}

// findInvite is constant-time per invite, like the Verify* methods.
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

var ErrNoSuchInvite = errors.New("hub: no invite by that name")

// WithdrawInvite removes an invite; members who joined through it stay.
func WithdrawInvite(path, id string) (*Org, error) {
	org, _, err := WithdrawInvites(path, []string{id})
	return org, err
}

// WithdrawInvites removes several links in one write, so withdrawing a
// drawer of stale ones is one command rather than one command each. It
// removes none of them if any name is not there: a typo that half-worked
// would be worse than one that did nothing.
func WithdrawInvites(path string, ids []string) (_ *Org, gone []string, err error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	org, err := update(path, func(o *Org) error {
		for _, id := range ids {
			if _, ok := o.Invite(id); !ok {
				return fmt.Errorf("%w: %s", ErrNoSuchInvite, id)
			}
		}
		kept := make([]Invite, 0, len(o.Invites))
		gone = gone[:0]
		for _, v := range o.Invites {
			if want[v.ID] {
				gone = append(gone, v.ID)
				continue
			}
			kept = append(kept, v)
		}
		o.Invites = kept
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return org, gone, nil
}

func (o *Org) Live(now time.Time) []Invite {
	var out []Invite
	for _, v := range o.Invites {
		if !v.Spent(now) {
			out = append(out, v)
		}
	}
	return out
}
