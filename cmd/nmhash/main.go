// nmhash: NeuroMorph hash of a FIXED, node-free vector — a cross-build determinism probe.
// The SAME source must yield the SAME hash on every toolchain the network agrees on. If the
// output differs between go1.25.0 (server: node + old pool) and go1.26.4 (local cross-build),
// the local binary computes hashes the network rejects → ~98% "low difficulty share". Read-only.
package main

import (
	"encoding/hex"
	"fmt"
	"runtime"

	"cereblix/core"
	nm "cereblix/neuromorph"
)

func hashAt(seed, header []byte, height, nonce uint64) string {
	vm := nm.NewVM(nm.DeriveParams(seed))
	h := make([]byte, len(header))
	copy(h, header)
	for i := 0; i < 8; i++ {
		h[core.NonceOffset+i] = byte(nonce >> (8 * i))
	}
	out := vm.Hash(h, height)
	return hex.EncodeToString(out[:])
}

func main() {
	fmt.Printf("toolchain=%s GOAMD64-build\n", runtime.Version())
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i*7 + 1)
	}
	header := make([]byte, core.HeaderLen)
	for i := range header {
		header[i] = byte(i)
	}
	// height 100 = dataset OFF (pure VM/float path); 10974 = dataset ON (prod-like, +64 MiB step).
	for _, height := range []uint64{100, 10974} {
		for _, nonce := range []uint64{1, 281474976710656, 12379813738877118345} {
			fmt.Printf("h=%-6d nonce=%-20d hash=%s\n", height, nonce, hashAt(seed, header, height, nonce))
		}
	}
}
