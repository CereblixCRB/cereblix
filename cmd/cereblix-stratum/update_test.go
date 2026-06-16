package main

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"cereblix/core"
)

func TestVerNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.1", "1.0", true},
		{"1.0", "1.1", false},
		{"1.0", "1.0", false},
		{"1.10.0", "1.9.0", true}, // numeric, not lexicographic
		{"2.0.0", "1.9.9", true},
		{"1.0.1", "1.0", true},
	}
	for _, c := range cases {
		if got := verNewer(c.a, c.b); got != c.want {
			t.Errorf("verNewer(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestShouldInstall(t *testing.T) {
	cases := []struct {
		manifest, cur, blocked string
		want                   bool
	}{
		{"1.1", "1.0", "", true},     // newer, nothing blocked
		{"1.0", "1.0", "", false},    // same
		{"0.9", "1.0", "", false},    // older
		{"1.1", "1.0", "1.1", false}, // newer but exactly the blocked one
		{"1.1", "1.0", "1.2", false}, // blocked is even newer
		{"1.3", "1.0", "1.1", true},  // a strictly-newer fix supersedes the blacklist
	}
	for _, c := range cases {
		if got := shouldInstall(c.manifest, c.cur, c.blocked); got != c.want {
			t.Errorf("shouldInstall(%q,%q,%q)=%v want %v", c.manifest, c.cur, c.blocked, got, c.want)
		}
	}
}

func TestDecideBoot(t *testing.T) {
	cases := []struct {
		name                      string
		hasPending, envOK, hasOld bool
		attempts                  int
		want                      bootDecision
	}{
		{"clean", false, false, false, 1, bootClean},
		{"watch first boot", true, true, true, 1, bootWatch},
		{"watch at threshold", true, true, true, maxBadBoots, bootWatch},
		{"rollback past threshold", true, true, true, maxBadBoots + 1, bootRollback},
		{"giveup no backup", true, true, false, maxBadBoots + 1, bootGiveUp},
		{"env busy never blames binary", true, false, true, maxBadBoots + 5, bootEnvBusy},
	}
	for _, c := range cases {
		if got := decideBoot(c.hasPending, c.envOK, c.hasOld, c.attempts); got != c.want {
			t.Errorf("%s: decideBoot=%d want %d", c.name, got, c.want)
		}
	}
}

// TestSelfUpdateProductGuard: we must never overwrite ourselves with a binary
// whose URL is not a cereblix-stratum binary (defense against an operator publishing
// the wrong manifest at the stratum URL). No network is touched on rejection.
func TestSelfUpdateProductGuard(t *testing.T) {
	err := selfUpdate(core.UpgradeBinary{
		URLs:   []string{"https://github.com/CereblixCRB/cereblix/releases/latest/download/cereblixd-linux-amd64"},
		SHA256: "deadbeef",
	})
	if err == nil || !strings.Contains(err.Error(), productGuard) {
		t.Fatalf("expected a %s product-guard error, got %v", productGuard, err)
	}
}

// TestFetchManifestRejectsForgedSignature: a manifest signed by a NON-authority key
// must be rejected, so a hostile mirror cannot push a binary.
func TestFetchManifestRejectsForgedSignature(t *testing.T) {
	// A deterministic key that is NOT the hardcoded authority key.
	priv := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	forged := core.SignManifest(core.UpgradeManifest{
		Version:  "9.9.9",
		Binaries: map[string]core.UpgradeBinary{platformKey(): {URL: "x", SHA256: "y"}},
	}, priv)
	if forged.Verify() {
		t.Fatal("precondition: forged manifest should NOT verify against the authority key")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(forged)
	}))
	defer srv.Close()
	os.Setenv("CEREBLIX_STRATUM_MANIFEST_URL", srv.URL)
	defer os.Unsetenv("CEREBLIX_STRATUM_MANIFEST_URL")

	if _, ok := fetchManifest(); ok {
		t.Fatal("fetchManifest accepted a forged-signature manifest")
	}
}

// TestFetchManifestRejectsGarbage: non-JSON body is ignored, not a crash.
func TestFetchManifestRejectsGarbage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()
	os.Setenv("CEREBLIX_STRATUM_MANIFEST_URL", srv.URL)
	defer os.Unsetenv("CEREBLIX_STRATUM_MANIFEST_URL")

	if _, ok := fetchManifest(); ok {
		t.Fatal("fetchManifest accepted garbage")
	}
}
