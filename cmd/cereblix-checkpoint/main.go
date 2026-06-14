// cereblix-checkpoint: the authority's signing tool. Periodically signs a block
// a few behind the tip with the authority key and pushes it to the node, which
// serves it to peers. Every node enforces it (no reorg across it; a block at
// that height must match), so the authority's chain stays canonical even
// against a higher-hashrate fork. Keep the authority key safe.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cereblix/core"
)

func getJSON(u string, o any) error {
	r, e := http.Get(u)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("http %d", r.StatusCode)
	}
	return json.NewDecoder(r.Body).Decode(o)
}

func main() {
	node := flag.String("node", "http://127.0.0.1:18751/api", "node API base")
	keyfile := flag.String("key", "/opt/cerebra/authority.key", "authority private key file (hex)")
	back := flag.Uint64("back", 20, "checkpoint this many blocks behind the tip (finality margin)")
	every := flag.Duration("every", 5*time.Minute, "signing interval (0 = run once)")
	flag.Parse()

	raw, err := os.ReadFile(*keyfile)
	if err != nil {
		log.Fatalf("read key: %v", err)
	}
	sk, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(sk) != ed25519.PrivateKeySize {
		log.Fatal("bad authority key (need 128-hex ed25519)")
	}
	priv := ed25519.PrivateKey(sk)
	log.Printf("checkpoint signer: node=%s back=%d every=%s", *node, *back, *every)

	sign := func() {
		var st struct {
			Height uint64 `json:"height"`
		}
		if err := getJSON(*node+"/status", &st); err != nil {
			log.Printf("status: %v", err)
			return
		}
		if st.Height <= *back {
			return
		}
		h := st.Height - *back
		var blk core.Block
		if err := getJSON(fmt.Sprintf("%s/block?h=%d", *node, h), &blk); err != nil {
			log.Printf("block %d: %v", h, err)
			return
		}
		cp := core.SignCheckpoint(h, blk.Hash(), priv)
		body, _ := json.Marshal(cp)
		resp, err := http.Post(*node+"/checkpoint", "application/json", strings.NewReader(string(body)))
		if err != nil {
			log.Printf("post: %v", err)
			return
		}
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		log.Printf("signed checkpoint h=%d hash=%s -> %s", h, blk.Hash()[:16], strings.TrimSpace(string(out)))
	}

	sign()
	if *every > 0 {
		for range time.Tick(*every) {
			sign()
		}
	}
}
