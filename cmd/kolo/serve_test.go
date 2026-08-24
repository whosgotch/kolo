package main

import "testing"

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
