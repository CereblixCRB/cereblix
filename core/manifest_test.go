package core

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

func TestUpgradeManifestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)

	m := UpgradeManifest{
		Version:    "2.0.0",
		MinVersion: "2.0.0",
		Notes:      "fee market network upgrade",
		Binaries: map[string]UpgradeBinary{
			"linux-amd64": {URL: "https://x/cereblixd-linux-amd64", SHA256: "abc123"},
		},
		Forks: []UpgradeFork{{Name: "feemarket", Height: FeeMarketHeight, MinVersion: "2.0.0"}},
	}
	signed := SignManifest(m, priv)

	if !signed.verifyWith(pubHex) {
		t.Fatal("authority-signed manifest must verify")
	}
	// Wrong key rejected.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if signed.verifyWith(hex.EncodeToString(otherPub)) {
		t.Fatal("manifest must NOT verify under a different key")
	}
	// Tamper after signing -> rejected.
	tampered := signed
	tampered.Version = "9.9.9"
	if tampered.verifyWith(pubHex) {
		t.Fatal("tampered version must invalidate the signature")
	}
	tampered2 := signed
	tampered2.Binaries = map[string]UpgradeBinary{"linux-amd64": {URL: "https://evil/x", SHA256: "abc123"}}
	if tampered2.verifyWith(pubHex) {
		t.Fatal("tampered binary URL must invalidate the signature")
	}
	// Unsigned / empty rejected.
	if (UpgradeManifest{Version: "2.0.0"}).verifyWith(pubHex) {
		t.Fatal("unsigned manifest must not verify")
	}
}
