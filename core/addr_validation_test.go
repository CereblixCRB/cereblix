package core

import (
	"strings"
	"testing"
)

// Locks the strict address validation the explorer/wallet/API rely on: a
// destination that is not exactly crb1 + 40 hex chars can never enter the
// mempool/chain, so node-served strings (rendered into the explorer/wallet DOM)
// cannot carry HTML/script/quote metacharacters. Defense-in-depth behind the
// client-side escaping; see /api/balance, /api/history (ValidAddr) + Tx.checkSig.
func TestValidAddrRejectsInjectionAndMalformed(t *testing.T) {
	bad := []string{
		"",
		"crb1",
		"crb1" + "0",                                   // too short
		"crb1" + strings.Repeat("0", 39),                       // 39 hex
		"crb1" + strings.Repeat("0", 41),                       // 41 hex
		"crb1" + strings.Repeat("0", 40) + "x",                 // trailing junk
		"crb1<script>alert(1)</script>",                // XSS attempt
		"crb1" + strings.Repeat("g", 40),                       // non-hex chars
		"crb1'\";<>&" + strings.Repeat("0", 31),                // metacharacters, right length
		"xyz1" + strings.Repeat("0", 40),                       // wrong prefix
		"CRB1" + strings.Repeat("0", 40),                       // prefix is case-sensitive
		" crb1" + strings.Repeat("0", 40),                      // leading space
		"crb1" + strings.Repeat("0", 20) + "\n" + strings.Repeat("0", 19),
	}
	for _, a := range bad {
		if ValidAddr(a) {
			t.Errorf("ValidAddr accepted malformed/injection address: %q", a)
		}
	}
	good := []string{
		"crb1" + strings.Repeat("0", 40),
		"crb1" + strings.Repeat("a", 40),
		"crb1" + strings.Repeat("F", 40), // upper hex accepted
		"crb1deadbeef" + strings.Repeat("0", 32),
	}
	for _, a := range good {
		if !ValidAddr(a) {
			t.Errorf("ValidAddr rejected a valid address: %q", a)
		}
	}
}

// Locks the transaction-acceptance gate: a non-coinbase tx with a malformed
// destination is rejected before any signature work, so the broadcast endpoint
// cannot be used to inject a tx with a metacharacter-laden `to`.
func TestCheckSigRejectsBadDestination(t *testing.T) {
	tx := &Tx{
		FromPub: strings.Repeat("00", 32), // 64 hex = 32 bytes, structurally valid pubkey
		To:      "crb1<script>",
		Amount:  1,
		Fee:     1,
		Nonce:   0,
		Sig:     strings.Repeat("00", 64),
	}
	if err := tx.CheckSig(); err == nil {
		t.Fatal("CheckSig accepted a tx with an injection destination address")
	}
}

