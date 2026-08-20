package main

import "testing"

// A listener asked for every interface reports itself as [::]:7300, whose first
// colon is inside the address rather than before the port. Cutting there gave
// kolo up a port of ":]:7300", which it joined into a loopback URL the host half
// spent the rest of its life failing to dial — so the machine never joined, and
// the org was offered nothing to run an agent on.
func TestPortOf(t *testing.T) {
	for _, c := range []struct{ addr, want string }{
		{"0.0.0.0:7300", "7300"},
		{"[::]:7300", "7300"},
		{"127.0.0.1:7300", "7300"},
		{"[::1]:443", "443"},
		{"7300", "7300"},
	} {
		if got := portOf(c.addr); got != c.want {
			t.Errorf("portOf(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}

func TestNextAddr(t *testing.T) {
	for _, c := range []struct{ addr, want string }{
		{"0.0.0.0:7300", "0.0.0.0:7301"},
		{"[::]:7300", "[::]:7301"},
		{"127.0.0.1:7300", "127.0.0.1:7301"},
		{"nonsense", "nonsense"},
	} {
		if got := nextAddr(c.addr); got != c.want {
			t.Errorf("nextAddr(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}
