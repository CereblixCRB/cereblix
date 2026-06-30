// Package mobile is the gomobile-bind surface for the Cereblix Android wallet.
//
// It exposes ONLY gomobile-compatible exported functions: every parameter and
// return value is one of string / int / int64 / bool / []byte (NO maps, NO
// struct slices, NO channels). Anything structured is returned as a JSON string.
//
// All cryptography is delegated to the SAME cereblix/core package the desktop
// CLI / GUI wallet uses, so keys, addresses and signatures are byte-for-byte
// identical across desktop and Android: ed25519 keys, the crb1 address scheme
// (sha256(pub)[:20]) and the "cerebra-tx-v1" / ChainID-bound signing payload.
// The Android app does key generation and signing ONLY through this package, so
// there is exactly one crypto implementation for the whole product.
package mobile

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"cereblix/core"
)

// CoinUnit returns synapses-per-CRB (core.CoinUnit) so the app converts a CRB
// amount to integer synapses with the EXACT unit the chain uses, instead of
// hardcoding it on the Kotlin side.
func CoinUnit() int64 { return int64(core.CoinUnit) }

// NewAddress generates a fresh ed25519 key pair and returns it as JSON:
//
//	{"label":"main","addr":"crb1...","priv":"<128-hex>"}
//
// priv is 128 hex chars (the 64-byte ed25519 private key) - the SAME encoding
// the desktop wallet.json stores, so a key created here imports there and back.
// Returns {"error":"..."} on the (practically impossible) keygen failure.
func NewAddress() string {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return `{"error":"keygen failed"}`
	}
	out := struct {
		Label string `json:"label"`
		Addr  string `json:"addr"`
		Priv  string `json:"priv"`
	}{
		Label: "main",
		Addr:  core.AddrFromPub(pub),
		Priv:  hex.EncodeToString(priv),
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// AddressFromPriv derives the crb1 address for a 128-hex ed25519 private key.
// Returns "" if privHex is not a valid 64-byte ed25519 private key (so the
// caller can validate an imported key before storing it).
func AddressFromPriv(privHex string) string {
	raw, err := hex.DecodeString(privHex)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return ""
	}
	priv := ed25519.PrivateKey(raw)
	return core.AddrFromPub(priv.Public().(ed25519.PublicKey))
}

// ValidateAddress reports whether addr is a structurally valid crb1 address
// (crb1 prefix + 40 hex chars). Thin wrapper over core.ValidAddr so the app and
// the chain agree exactly on what a sendable address looks like.
func ValidateAddress(addr string) bool { return core.ValidAddr(addr) }

// SignSend builds and LOCALLY signs a payment transaction, returning the signed
// core.Tx as JSON ready to HTTP POST to the node's /tx endpoint:
//
//	{"from_pub":"...","to":"...","amount":N,"fee":N,"nonce":N,"sig":"..."}
//
// Parameters (all int64 because gomobile has no uint64):
//   - privHex: 128-hex ed25519 private key of the sender (never leaves the device)
//   - to:      crb1 destination address
//   - amount:  synapses to send  (CRB * CoinUnit())
//   - fee:     synapses fee       (use GET /status fee_suggested; >= 0)
//   - nonce:   sender's current account nonce (GET /balance -> nonce)
//   - height:  height the tx will be mined at (network height + 1); selects the
//     ChainID-bound signing payload from core.ChainIDHeight on, so the signature
//     is bound to THIS chain and cannot be replayed onto a fork/testnet.
//
// On bad input it returns {"error":"..."} (the Kotlin side checks for an
// "error" field vs the presence of "sig"). The private key NEVER leaves the
// device - signing is entirely in-process here.
func SignSend(privHex, to string, amount, fee, nonce, height int64) string {
	raw, err := hex.DecodeString(privHex)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return `{"error":"bad private key"}`
	}
	if !core.ValidAddr(to) {
		return `{"error":"bad destination address"}`
	}
	if amount <= 0 || fee < 0 || nonce < 0 || height < 0 {
		return `{"error":"bad amount/fee/nonce/height"}`
	}
	tx := &core.Tx{
		To:     to,
		Amount: uint64(amount),
		Fee:    uint64(fee),
		Nonce:  uint64(nonce),
	}
	core.SignTxAt(tx, ed25519.PrivateKey(raw), uint64(height))
	b, err := json.Marshal(tx)
	if err != nil {
		return `{"error":"marshal failed"}`
	}
	return string(b)
}

// VerifyManifest reports whether sigHex is a valid ed25519 signature, produced by
// the holder of the private key matching pubHex (a 32-byte ed25519 public key,
// hex), over the raw UTF-8 bytes of payload.
//
// The Android wallet uses this to AUTHENTICATE the signed auto-update manifest
// before ever showing an "update available" banner. Reusing crypto/ed25519 here
// (the SAME library the node and the rest of this binding use) keeps verification
// trivially correct on every Android API level, instead of relying on the
// java.security Ed25519 provider that only exists on API 33+. It is fail-closed:
// any malformed/short input returns false, and the app shows NO banner.
func VerifyManifest(pubHex, payload, sigHex string) bool {
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), []byte(payload), sig)
}
