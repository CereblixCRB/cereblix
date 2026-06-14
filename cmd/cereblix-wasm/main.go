//go:build js && wasm

// cereblix-wasm compiles the NeuroMorph hasher to WebAssembly so the coin can
// be mined in any browser - desktop, Android, or iOS - with no app, no app
// store and no code signing. It exposes one function to JavaScript:
//
//	cereblixMine(headerHex, targetHex, seedHex, height, startNonceStr, count)
//	  -> {found:true, nonce:"<dec>"} | {found:false, hashed:N, next:"<dec>"}
//
// Hashing is byte-identical to the amd64 node (verified by TestCrossPlatformHash),
// so nonces found here are accepted by the network.
package main

import (
	"encoding/hex"
	"math/big"
	"strconv"
	"syscall/js"

	"cereblix/core"
	nm "cereblix/neuromorph"
)

// One reusable VM per epoch seed (rebuilding it - and the 64 MiB dataset - is
// expensive, so we keep it across calls within an epoch).
var (
	curVM   *nm.VM
	curSeed string
)

func u64(s string) uint64 { v, _ := strconv.ParseUint(s, 10, 64); return v }

func mine(_ js.Value, args []js.Value) any {
	header, err := hex.DecodeString(args[0].String())
	if err != nil || len(header) != core.HeaderLen {
		return map[string]any{"err": "bad header"}
	}
	targetRaw, err := hex.DecodeString(args[1].String())
	if err != nil || len(targetRaw) != 32 {
		return map[string]any{"err": "bad target"}
	}
	target := new(big.Int).SetBytes(targetRaw)
	seedHex := args[2].String()
	height := uint64(args[3].Int())
	nonce := u64(args[4].String())
	count := args[5].Int()

	if seedHex != curSeed || curVM == nil {
		seed, err := hex.DecodeString(seedHex)
		if err != nil {
			return map[string]any{"err": "bad seed"}
		}
		curVM = nm.NewVM(nm.DeriveParams(seed))
		curSeed = seedHex
	}

	for i := 0; i < count; i++ {
		for b := 0; b < 8; b++ {
			header[core.NonceOffset+b] = byte(nonce >> (8 * b))
		}
		h := curVM.Hash(header, height)
		if new(big.Int).SetBytes(h[:]).Cmp(target) <= 0 {
			return map[string]any{"found": true, "nonce": strconv.FormatUint(nonce, 10)}
		}
		nonce++
	}
	return map[string]any{"found": false, "hashed": count, "next": strconv.FormatUint(nonce, 10)}
}

func main() {
	js.Global().Set("cereblixMine", js.FuncOf(mine))
	select {} // keep the Go runtime alive for JS calls
}
