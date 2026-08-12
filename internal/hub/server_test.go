package hub

import (
	"net/http"
	"testing"
)

func hubFixture(t *testing.T) (*Server, string) {
	t.Helper()
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	org := &Org{Name: "acme", Members: []Member{{ID: "artem", Name: "Artem", TokenHash: hash}}}

	s, err := Listen(org, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return s, token
}

func TestAuthenticate(t *testing.T) {
	s, token := hubFixture(t)

	for _, tc := range []struct {
		name   string
		header string
		want   bool
	}{
		{"a member's token", "Bearer " + token, true},
		{"no header at all", "", false},
		{"the token without the scheme", token, false},
		{"a token nobody was issued", "Bearer kolo_nobody", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest("GET", "/", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if _, ok := s.authenticate(r); ok != tc.want {
				t.Errorf("authenticate = %v, want %v", ok, tc.want)
			}
		})
	}
}
