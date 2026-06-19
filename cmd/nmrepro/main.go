// nmrepro: isolate the parallel-validation bug. Fetches REAL work from the prod node
// (epoch seed + height + header), then computes hashes (a) single-threaded and (b) with the
// same VM-pool + concurrency the pool used, comparing both to a fresh-VM reference. If the
// concurrent path mismatches while the serialized one matches, the bug is concurrency in
// nm.Hash sharing per-epoch state (Params/dataset). Read-only: only getwork. Throwaway tool.
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"cereblix/core"
	nm "cereblix/neuromorph"
)

func hdrFor(header []byte, nonce uint64) []byte {
	h := make([]byte, len(header))
	copy(h, header)
	for i := 0; i < 8; i++ {
		h[core.NonceOffset+i] = byte(nonce >> (8 * i))
	}
	return h
}

func run(label string, params *nm.Params, header []byte, height uint64, ref map[uint64][32]byte, conc, goroutines, nonces int) {
	sem := make(chan struct{}, conc)
	pool := sync.Pool{New: func() any { return nm.NewVM(params) }}
	var wg sync.WaitGroup
	var mu sync.Mutex
	mism := 0
	var first string
	t0 := time.Now()
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < nonces; n++ {
				sem <- struct{}{}
				vm := pool.Get().(*nm.VM)
				got := vm.Hash(hdrFor(header, uint64(n)), height)
				pool.Put(vm)
				<-sem
				if got != ref[uint64(n)] {
					mu.Lock()
					mism++
					if first == "" {
						first = fmt.Sprintf("nonce %d", n)
					}
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	el := time.Since(t0)
	total := goroutines * nonces
	rate := float64(total) / el.Seconds()
	fmt.Printf("  [%-11s] concurrency=%d  hashes=%d  time=%5.2fs  THROUGHPUT=%6.0f hash/s  mismatches=%d %s\n",
		label, conc, total, el.Seconds(), rate, mism, first)
}

func main() {
	resp, err := http.Get("http://127.0.0.1:18751/api/getwork?addr=crb1ac25dfff5a231631ef73a6c73df93ade1ad55c9e")
	if err != nil {
		panic(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var gw struct {
		Header string `json:"header"`
		Seed   string `json:"seed"`
		Height uint64 `json:"height"`
	}
	if e := json.Unmarshal(body, &gw); e != nil {
		panic(e)
	}
	header, _ := hex.DecodeString(gw.Header)
	seed, _ := hex.DecodeString(gw.Seed)
	params := nm.DeriveParams(seed)
	fmt.Printf("REAL work: height=%d  headerLen=%d  seedLen=%d  DatasetHeight=%d (dataset %s)\n",
		gw.Height, len(header), len(seed), nm.DatasetHeight, map[bool]string{true: "ON", false: "off"}[gw.Height >= nm.DatasetHeight])

	// reference: one fresh VM, sequential (this is what the miner effectively computes)
	refVM := nm.NewVM(params)
	ref := map[uint64][32]byte{}
	const N = 80
	for n := uint64(0); n < N; n++ {
		ref[n] = refVM.Hash(hdrFor(header, n), gw.Height)
	}

	run("serialized", params, header, gw.Height, ref, 1, 8, N)  // control: must be 0 mismatches
	run("parallel-6", params, header, gw.Height, ref, 6, 16, N) // the suspect
	run("parallel-8", params, header, gw.Height, ref, 8, 24, N)
}
