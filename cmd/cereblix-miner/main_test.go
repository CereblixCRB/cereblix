package main

import "testing"

func TestNormalizeNodeURL(t *testing.T) {
	ok := []struct{ in, want string }{
		{"https://cereblix.com/pool/api", "https://cereblix.com/pool/api"},
		{"http://127.0.0.1:18751/api", "http://127.0.0.1:18751/api"},
		{"cereblix.com/pool/api", "https://cereblix.com/pool/api"},             // missing scheme -> https
		{"  https://cereblix.com/pool/api  ", "https://cereblix.com/pool/api"}, // trimmed
		{"ru.cereblix.com/pool/api", "https://ru.cereblix.com/pool/api"},
		{"http://1.2.3.4:18751/api", "http://1.2.3.4:18751/api"},
		{"", ""},
	}
	for _, c := range ok {
		got, err := normalizeNodeURL(c.in)
		if err != nil {
			t.Errorf("normalizeNodeURL(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeNodeURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Stratum endpoints must be rejected (the native miner can't speak Stratum).
	bad := []string{
		"Stratum.cereblix.com:3334",
		"stratum.cereblix.com:3333",
		"stratum+tcp://1.2.3.4:3333",
		"stratum://host:3334",
		"1.2.3.4:3334",          // bare stratum port
		"http://1.2.3.4:3333/x", // stratum port even with a scheme/path
	}
	for _, in := range bad {
		if _, err := normalizeNodeURL(in); err == nil {
			t.Errorf("normalizeNodeURL(%q) should have errored (Stratum endpoint)", in)
		}
	}
}
