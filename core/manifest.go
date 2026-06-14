package core

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
)

// UpgradeManifest is an authority-signed release announcement that lets nodes
// auto-update SAFELY without trusting any single download mirror. It is served
// from GitHub, this node's /api/upgrade and peers' /p2p/upgrade. A node accepts
// it only if Verify() passes against the hardcoded AuthorityPubKey (the same key
// used for checkpoints), so a hostile mirror or a MITM cannot push a malicious
// binary: the binary URL AND its sha256 are inside the signed payload, and the
// node refuses any download whose hash does not match. Forks lists scheduled
// consensus changes (activation floor height + the node version that supports
// them) purely for operator visibility and countdown warnings - the actual
// activation is readiness-gated in consensus (see upgrade.go).
type UpgradeBinary struct {
	// URLs is the preferred, ordered list of mirrors to try (GitHub first, our
	// Cloudflare origin as fallback). URL is a single-mirror fallback kept for
	// older nodes that predate URLs. The node tries each until the sha256 matches.
	URLs   []string `json:"urls,omitempty"`
	URL    string   `json:"url"`
	SHA256 string   `json:"sha256"`
}

type UpgradeFork struct {
	Name       string `json:"name"`
	Height     uint64 `json:"height"`
	MinVersion string `json:"min_version"`
}

type UpgradeManifest struct {
	Version    string                   `json:"version"`     // latest release version
	MinVersion string                   `json:"min_version"` // urge update below this
	Notes      string                   `json:"notes"`
	Binaries   map[string]UpgradeBinary `json:"binaries"` // "linux-amd64" -> binary
	Forks      []UpgradeFork            `json:"forks"`
	Sig        string                   `json:"sig"` // hex ed25519 over the unsigned form
}

// signingBytes is the canonical payload the signature covers: the manifest as
// JSON with Sig emptied, prefixed with a domain tag. Deterministic because Go
// marshals struct fields in declaration order and map keys sorted.
func (m UpgradeManifest) signingBytes() []byte {
	u := m
	u.Sig = ""
	b, _ := json.Marshal(u)
	return append([]byte("cereblix-upgrade-v1|"), b...)
}

func (m UpgradeManifest) verifyWith(pubHex string) bool {
	if pubHex == "" || m.Version == "" || m.Sig == "" {
		return false
	}
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(m.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), m.signingBytes(), sig)
}

// Verify checks the manifest's signature against the hardcoded authority key.
func (m UpgradeManifest) Verify() bool { return m.verifyWith(AuthorityPubKey) }

// SignManifest signs a manifest with the authority private key.
func SignManifest(m UpgradeManifest, priv ed25519.PrivateKey) UpgradeManifest {
	m.Sig = ""
	m.Sig = hex.EncodeToString(ed25519.Sign(priv, m.signingBytes()))
	return m
}
