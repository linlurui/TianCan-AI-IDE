package process

import (
	"testing"
)

func TestExtractConflictPort(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		// exact message from fast-api
		{
			"fast-api failed to start on port 8808: listen tcp 0.0.0.0:8808: bind: address already in use",
			"8808",
		},
		// standard Go net error
		{
			"listen tcp 0.0.0.0:3000: bind: address already in use",
			"3000",
		},
		// Node EADDRINUSE (port at end)
		{
			"Error: listen EADDRINUSE: address already in use :::5000",
			"5000",
		},
		// should NOT match (no conflict keyword)
		{
			"fast-api attempting to bind to 0.0.0.0:8808",
			"",
		},
	}
	for _, c := range cases {
		got := extractConflictPort(c.line)
		if got != c.want {
			t.Errorf("extractConflictPort(%q): got %q, want %q", c.line, got, c.want)
		}
	}
}

func TestPortBindRe(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"fast-api attempting to bind to 0.0.0.0:8808...", "8808"},
		{"listening on 127.0.0.1:5000", "5000"},
		{"Server started on [::]:9090 (HTTP)", "9090"},
		{"localhost:8080 ready", "8080"},
		// should NOT match
		{"no port here", ""},
	}
	for _, c := range cases {
		m := portBindRe.FindStringSubmatch(c.line)
		got := ""
		if len(m) >= 2 {
			got = m[1]
		}
		if got != c.want {
			t.Errorf("portBindRe(%q): got %q, want %q", c.line, got, c.want)
		}
	}
}

func TestAddPortDedup(t *testing.T) {
	s := &Service{}
	s.addPort("/proj", "8808")
	s.addPort("/proj", "8808")
	s.addPort("/proj", "3000")
	ports := s.cachedPorts("/proj")
	if len(ports) != 2 {
		t.Errorf("expected 2 unique ports, got %d: %v", len(ports), ports)
	}
}
