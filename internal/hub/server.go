package hub

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Server is the hub: the org, and the routes that let members and hosts reach it.
type Server struct {
	org *Org
	ln  net.Listener
	srv *http.Server
}

// Listen binds addr, a host:port. Use a host of 0.0.0.0 to accept connections
// from other machines.
func Listen(org *Org, addr string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("hub: listen: %w", err)
	}
	return &Server{org: org, ln: ln, srv: &http.Server{Handler: http.NewServeMux()}}, nil
}

// Addr is where the hub is listening, with the port resolved if one was asked
// for by asking for zero.
func (s *Server) Addr() string { return s.ln.Addr().String() }

func (s *Server) Serve() error {
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Close() error { return s.srv.Close() }

// authenticate resolves the bearer token on a request to a member.
//
// The token travels in a header rather than in the URL, because a URL is written
// to the access log of every proxy between the two machines, and to the hub's own.
func (s *Server) authenticate(r *http.Request) (Member, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return Member{}, false
	}
	return s.org.VerifyMember(token)
}
