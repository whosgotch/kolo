package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// TokenPrefix marks a string as a kolo token wherever it turns up. Secret
// scanners key off prefixes like this, and so do people.
const TokenPrefix = "kolo_"

// Far past what anyone can guess, and machine-handled rather than typed, so the
// length costs nothing.
const tokenBytes = 32

// NewToken returns a token to give to a member and the hash to record for them.
// kolo never stores the token: this is the only moment it exists, and losing it
// means issuing another.
func NewToken() (token, hash string, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("hub: generate token: %w", err)
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// JoinPrefix marks a host's join string. It carries the kolo_ prefix too, so a
// scanner that knows one knows the other.
const JoinPrefix = TokenPrefix + "join_"

// join is what a machine needs to reach an org: where the hub is, and the token
// to arrive with. One string rather than two, because two halves sent separately
// end up in the same message anyway, and are one more thing to get right by hand.
type join struct {
	Hub   string `json:"hub"`
	Token string `json:"token"`
}

// NewJoin packs a hub and a host's token into one string to paste.
func NewJoin(hubURL, token string) string {
	b, _ := json.Marshal(join{Hub: hubURL, Token: token})
	return JoinPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParseJoin reads a join string back. It is not a security boundary — the token
// inside is checked by the hub like any other — so what is refused here is only
// what would otherwise fail later with nothing to point at.
func ParseJoin(s string) (hubURL, token string, err error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, JoinPrefix) {
		return "", "", fmt.Errorf("hub: not a join string: one starts with %s", JoinPrefix)
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, JoinPrefix))
	if err != nil {
		return "", "", fmt.Errorf("hub: this join string is damaged; ask for another")
	}
	var j join
	if err := json.Unmarshal(b, &j); err != nil {
		return "", "", fmt.Errorf("hub: this join string is damaged; ask for another")
	}
	if j.Hub == "" || !strings.HasPrefix(j.Token, TokenPrefix) {
		return "", "", fmt.Errorf("hub: this join string carries no hub and token; ask for another")
	}
	return j.Hub, j.Token, nil
}

// HashToken returns the hex SHA-256 of a token.
//
// A plain hash, not bcrypt or argon2: those make guessing a low-entropy secret
// expensive, and 32 random bytes offer nothing to guess. What matters is that the
// stored form cannot be used to connect.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}
