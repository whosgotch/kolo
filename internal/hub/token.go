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

// TokenPrefix marks a string as a kolo token, for secret scanners.
const TokenPrefix = "kolo_"

const tokenBytes = 32

// NewToken returns a token and its hash; the raw token is never stored.
func NewToken() (token, hash string, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("hub: generate token: %w", err)
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

const JoinPrefix = TokenPrefix + "join_"

type join struct {
	Hub   string `json:"hub"`
	Token string `json:"token"`
}

func NewJoin(hubURL, token string) string {
	b, _ := json.Marshal(join{Hub: hubURL, Token: token})
	return JoinPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParseJoin decodes a join string. Not a security boundary: the token inside
// is verified by the hub like any other.
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

// HashToken returns the hex SHA-256 of a token; plain, since 32 random bytes
// leave nothing for bcrypt/argon2 to slow down.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// InviteURL puts the token in the fragment, which browsers never send to a
// server, keeping it out of logs.
func InviteURL(hubURL, token string) string {
	return strings.TrimSuffix(hubURL, "/") + "/join#" + token
}
