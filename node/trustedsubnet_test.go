package node

import (
	"net"
	"testing"
)

// The SSRF guard must stay fully closed by default, and -trustedsubnet must open
// ONLY the operator-declared range — never loopback, the cloud-metadata IP, or
// any other private range. This is a security boundary: lock it down with a test.
func TestTrustedSubnetExemptsOnlyDeclaredRange(t *testing.T) {
	// Default (no opt-in): every private/loopback/metadata address is rejected.
	SetTrustedSubnets("")
	for _, u := range []string{
		"http://10.10.0.4:18750",
		"http://192.168.1.1:18750",
		"http://127.0.0.1:18750",
		"http://169.254.169.254:18750", // cloud metadata
	} {
		if peerHostAllowed(u) {
			t.Fatalf("with no trusted subnet, %s must be rejected", u)
		}
	}
	if !peerHostAllowed("http://188.34.181.191:18750") {
		t.Fatal("a public IP must always be allowed")
	}

	// Opt in to one WG mesh: only that /24 becomes reachable; everything else
	// the guard blocks STAYS blocked.
	SetTrustedSubnets("10.10.0.0/24")
	defer SetTrustedSubnets("") // never leak global state into other tests
	if !peerHostAllowed("http://10.10.0.4:18750") {
		t.Fatal("a peer inside the trusted subnet must be allowed")
	}
	for _, u := range []string{
		"http://10.20.0.4:18750",       // private, but outside the declared /24
		"http://192.168.1.1:18750",     // a different private range
		"http://127.0.0.1:18750",       // loopback
		"http://169.254.169.254:18750", // cloud metadata MUST remain blocked
	} {
		if peerHostAllowed(u) {
			t.Fatalf("with only 10.10.0.0/24 trusted, %s must STILL be rejected", u)
		}
	}
}

// Invalid CIDRs are skipped, not fatal, so one typo can't drop the whole mesh.
func TestSetTrustedSubnetsSkipsJunk(t *testing.T) {
	SetTrustedSubnets("not-a-cidr, 10.10.0.0/24 ,")
	defer SetTrustedSubnets("")
	if !ipTrusted(net.ParseIP("10.10.0.7")) {
		t.Fatal("a valid CIDR among junk should still load")
	}
	if ipTrusted(net.ParseIP("8.8.8.8")) {
		t.Fatal("a public IP must never be trusted")
	}
}
