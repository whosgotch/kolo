package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// TokenPrefix marks a string as a kolo token wherever it turns up — in a shell
// history, a log, a paste. Secret scanners key off prefixes like this, and so do
// people.
const TokenPrefix = "kolo_"

// tokenBytes is the amount of randomness in a token. 32 bytes is far past what
// anyone can guess, and the token is machine-handled rather than typed, so
// length costs nothing.
const tokenBytes = 32

// NewToken returns a token to give to a member and the hash to record for them.
// The token itself is never stored anywhere by kolo: this is the only moment it
// exists, and if it is lost the answer is to issue another one.
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
// A plain hash, not bcrypt or argon2. Those exist to make guessing a
// *low-entropy* secret expensive — a password someone chose. A token is 32
// random bytes, so there is nothing to guess and nothing a slow hash would buy;
// what matters is that the stored form cannot be used to connect.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}
