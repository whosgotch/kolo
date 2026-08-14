package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// HashToken returns the hex SHA-256 of a token.
//
// A plain hash, not bcrypt or argon2: those make guessing a low-entropy secret
// expensive, and 32 random bytes offer nothing to guess. What matters is that the
// stored form cannot be used to connect.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}
