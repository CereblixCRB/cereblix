// cereblix-faucet: a small CRB faucet whose anti-bot captcha is a REAL NeuroMorph
// share. The browser mines one share (via the WASM hasher) against a template
// that pays the treasury wallet - the same wallet that funds the faucet. So the
// work claimants do is useful: it's genuine proof-of-work in our own algorithm,
// and if a share also meets the network target it becomes a real block paying
// the treasury. Gives a tiered amount once per 3h per address AND per IP.
// Listens on localhost behind the Apache reverse proxy.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"cereblix/core"
	nm "cereblix/neuromorph"
)

const cooldown = 3 * time.Hour

var (
	nodeAPI                     string
	amtBase, amtMiner, amtWhale uint64
	priv                        ed25519.PrivateKey
	from                        string // payout wallet (treasury), signs faucet payouts
	captchaAddr                 string // the captcha share mines to THIS wallet (separate)
	shareTarget                 *big.Int // fixed, easy target for the captcha share
	sendMu                      sync.Mutex
	nextNonce                   uint64
	store                       *limitStore
	poolAPI                     string  // if set, each solved captcha credits a pool share to captchaAddr
	creditSecret                string  // shared secret for the pool's /api/credit
	creditShares                float64 // share weight credited per solved captcha
)

// ----------------------------------------------------------- rate-limit store

type limitStore struct {
	mu   sync.Mutex
	path string
	Addr map[string]int64 `json:"addr"`
	IP   map[string]int64 `json:"ip"`
}

func loadStore(path string) *limitStore {
	s := &limitStore{path: path, Addr: map[string]int64{}, IP: map[string]int64{}}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, s)
	}
	if s.Addr == nil {
		s.Addr = map[string]int64{}
	}
	if s.IP == nil {
		s.IP = map[string]int64{}
	}
	return s
}

func (s *limitStore) save() {
	raw, _ := json.Marshal(s)
	_ = os.WriteFile(s.path, raw, 0o600)
}

func (s *limitStore) remaining(addr, ip string, now int64) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := now - int64(cooldown.Seconds())
	for k, v := range s.Addr {
		if v < cut {
			delete(s.Addr, k)
		}
	}
	for k, v := range s.IP {
		if v < cut {
			delete(s.IP, k)
		}
	}
	var last int64
	if v, ok := s.Addr[addr]; ok && v > last {
		last = v
	}
	if v, ok := s.IP[ip]; ok && v > last {
		last = v
	}
	if last == 0 {
		return 0
	}
	left := time.Duration(last+int64(cooldown.Seconds())-now) * time.Second
	if left < 0 {
		return 0
	}
	return left
}

func (s *limitStore) record(addr, ip string, now int64) {
	s.mu.Lock()
	s.Addr[addr] = now
	s.IP[ip] = now
	s.mu.Unlock()
	s.save()
}

// reserve atomically checks the cooldown and, if free, records the claim under a
// single lock. It returns left>0 (and records nothing) if the addr or IP is still
// cooling down. This closes the check-then-send-then-record race where N
// concurrent claims could all pass an independent remaining() check and each
// trigger a payout, draining the treasury by N× per window.
func (s *limitStore) reserve(addr, ip string, now int64) time.Duration {
	s.mu.Lock()
	cut := now - int64(cooldown.Seconds())
	for k, v := range s.Addr {
		if v < cut {
			delete(s.Addr, k)
		}
	}
	for k, v := range s.IP {
		if v < cut {
			delete(s.IP, k)
		}
	}
	var last int64
	if v, ok := s.Addr[addr]; ok && v > last {
		last = v
	}
	if v, ok := s.IP[ip]; ok && v > last {
		last = v
	}
	if last != 0 {
		if left := time.Duration(last+int64(cooldown.Seconds())-now) * time.Second; left > 0 {
			s.mu.Unlock()
			return left
		}
	}
	// free -> claim the slot immediately so concurrent requests see it taken
	s.Addr[addr] = now
	s.IP[ip] = now
	s.mu.Unlock()
	s.save()
	return 0
}

// release rolls back a reservation when the payout failed, so a transient send
// error does not lock the user out for the whole cooldown.
func (s *limitStore) release(addr, ip string) {
	s.mu.Lock()
	delete(s.Addr, addr)
	delete(s.IP, ip)
	s.mu.Unlock()
	s.save()
}

// --------------------------------------------------- NeuroMorph share captcha

type fwork struct {
	nodeID    string
	header    []byte
	seed      []byte
	height    uint64
	netTarget *big.Int
	exp       int64
	seen      map[uint64]bool
}

var (
	chMu       sync.Mutex
	challenges = map[string]*fwork{}
	vmMu       sync.Mutex
	vm         *nm.VM
	vmEpoch    uint64 = ^uint64(0)
)

func nodeGet(path string, out any) error {
	resp, err := http.Get(nodeAPI + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("node http %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// newChallenge fetches a template paying the treasury and stores it under a
// fresh one-time challenge id. The browser mines it to the (easy) shareTarget.
func newChallenge() (string, *fwork, error) {
	var gw struct {
		ID, Header, Target, Seed string
		Height                   uint64
	}
	if err := nodeGet("/getwork?addr="+captchaAddr, &gw); err != nil {
		return "", nil, err
	}
	header, e1 := hex.DecodeString(gw.Header)
	seed, e2 := hex.DecodeString(gw.Seed)
	netT, ok := new(big.Int).SetString(gw.Target, 16)
	if e1 != nil || e2 != nil || !ok || len(header) != core.HeaderLen {
		return "", nil, errors.New("bad template")
	}
	idb := make([]byte, 16)
	_, _ = rand.Read(idb)
	id := hex.EncodeToString(idb)
	fw := &fwork{nodeID: gw.ID, header: header, seed: seed, height: gw.Height,
		netTarget: netT, exp: time.Now().Unix() + 300, seen: map[uint64]bool{}}
	chMu.Lock()
	now := time.Now().Unix()
	if len(challenges) > 5000 {
		challenges = map[string]*fwork{}
	}
	for k, w := range challenges {
		if w.exp < now {
			delete(challenges, k)
		}
	}
	challenges[id] = fw
	chMu.Unlock()
	return id, fw, nil
}

func hashFor(fw *fwork, nonce uint64) [32]byte {
	hdr := make([]byte, len(fw.header))
	copy(hdr, fw.header)
	for i := 0; i < 8; i++ {
		hdr[core.NonceOffset+i] = byte(nonce >> (8 * i))
	}
	epoch := fw.height / core.EpochLength
	vmMu.Lock()
	if vm == nil || epoch != vmEpoch {
		vm = nm.NewVM(nm.DeriveParams(fw.seed))
		vmEpoch = epoch
	}
	h := vm.Hash(hdr, fw.height)
	vmMu.Unlock()
	return h
}

// ------------------------------------------------------------------ payout

func amountFor(addr string) (uint64, int) {
	var r struct {
		Blocks int `json:"blocks"`
	}
	_ = nodeGet("/mined?addr="+addr, &r)
	switch {
	case r.Blocks > 100:
		return amtWhale, r.Blocks
	case r.Blocks >= 1:
		return amtMiner, r.Blocks
	default:
		return amtBase, r.Blocks
	}
}

func sendCRB(to string, amount uint64) (string, error) {
	sendMu.Lock()
	defer sendMu.Unlock()
	var stt struct {
		Height uint64 `json:"height"`
		Fee    uint64 `json:"fee_suggested"`
	}
	if err := nodeGet("/status", &stt); err != nil {
		return "", fmt.Errorf("node unreachable")
	}
	var acc struct {
		Balance uint64 `json:"balance"`
		Nonce   uint64 `json:"nonce"`
	}
	if err := nodeGet("/balance?addr="+from, &acc); err != nil {
		return "", fmt.Errorf("node unreachable")
	}
	fee := stt.Fee
	if fee == 0 {
		fee = 1000
	}
	if acc.Balance < amount+fee {
		return "", fmt.Errorf("faucet is empty right now, try later")
	}
	nonce := acc.Nonce
	if nextNonce > nonce {
		nonce = nextNonce
	}
	tx := &core.Tx{To: to, Amount: amount, Fee: fee, Nonce: nonce}
	core.SignTxAt(tx, priv, stt.Height+1)
	body, _ := json.Marshal(tx)
	resp, err := http.Post(nodeAPI+"/tx", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("node unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r struct {
		TxID  string `json:"txid"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &r)
	if r.Error != "" {
		if strings.Contains(r.Error, "nonce") {
			nextNonce = 0
		}
		return "", fmt.Errorf("rejected: %s", r.Error)
	}
	nextNonce = nonce + 1
	return r.TxID, nil
}

// ------------------------------------------------------------------- handlers

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1]) // last hop = added by our Apache
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// creditPoolShare tells the pool (over localhost) to credit the captcha wallet a
// share. Best-effort: a failure never blocks the user's faucet claim.
func creditPoolShare(addr string, shares float64) {
	c := &http.Client{Timeout: 6 * time.Second}
	u := fmt.Sprintf("%s/credit?addr=%s&shares=%g", poolAPI, addr, shares)
	req, err := http.NewRequest("POST", u, nil)
	if err != nil {
		return
	}
	req.Header.Set("X-Credit-Secret", creditSecret)
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
	}
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18753", "listen address")
	flag.StringVar(&nodeAPI, "node", "http://127.0.0.1:18751/api", "node API base")
	keyfile := flag.String("keyfile", "/opt/cerebra/faucet-wallet.txt", "treasury wallet file with PRIVATE KEY line")
	base := flag.Float64("base", 0.001, "CRB for addresses that mined 0 blocks")
	miner := flag.Float64("miner", 0.01, "CRB for addresses that mined >=1 block")
	whale := flag.Float64("whale", 0.1, "CRB for addresses that mined >100 blocks")
	work := flag.Uint64("work", 800, "captcha share difficulty in expected hashes (browser NeuroMorph)")
	captcha := flag.String("captcha-addr", "", "address the captcha share mines to (defaults to payout wallet)")
	datadir := flag.String("datadir", "/var/lib/cerebra", "where to store rate-limit state")
	pool := flag.String("pool", "", "pool API base; if set, each solved captcha credits a pool share to the captcha wallet")
	creditSecretFile := flag.String("credit-secret-file", "", "file with the shared secret for the pool's /api/credit")
	creditW := flag.Float64("credit-shares", 1.0, "share weight credited to the captcha wallet per solved captcha")
	flag.Parse()

	poolAPI = strings.TrimRight(*pool, "/")
	creditShares = *creditW
	if *creditSecretFile != "" {
		if b, err := os.ReadFile(*creditSecretFile); err == nil {
			creditSecret = strings.TrimSpace(string(b))
		}
	}

	amtBase = uint64(*base * float64(core.CoinUnit))
	amtMiner = uint64(*miner * float64(core.CoinUnit))
	amtWhale = uint64(*whale * float64(core.CoinUnit))
	// shareTarget = 2^256 / work  (an easy target so a browser finds one share
	// in a handful of seconds, independent of network difficulty).
	if *work < 1 {
		*work = 1
	}
	shareTarget = new(big.Int).Div(new(big.Int).Lsh(big.NewInt(1), 256), new(big.Int).SetUint64(*work))

	raw, err := os.ReadFile(*keyfile)
	if err != nil {
		log.Fatalf("read keyfile: %v", err)
	}
	var skHex string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "PRIVATE KEY") {
			f := strings.Fields(line)
			skHex = f[len(f)-1]
		}
	}
	sk, err := hex.DecodeString(strings.TrimSpace(skHex))
	if err != nil || len(sk) != ed25519.PrivateKeySize {
		log.Fatalf("bad private key in keyfile")
	}
	priv = ed25519.PrivateKey(sk)
	from = core.AddrFromPub(priv.Public().(ed25519.PublicKey))
	captchaAddr = *captcha
	if !core.ValidAddr(captchaAddr) {
		captchaAddr = from
	}
	store = loadStore(*datadir + "/faucet.json")
	log.Printf("faucet: payout %s, captcha mines to %s, tiers %d/%d/%d, 3h cooldown, work=%d, listen %s",
		from, captchaAddr, amtBase, amtMiner, amtWhale, *work, *listen)

	mux := http.NewServeMux()
	mux.HandleFunc("/faucet/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"from": from, "cooldown_h": 3,
			"base": amtBase, "miner": amtMiner, "whale": amtWhale})
	})
	// /faucet/challenge?addr=... : checks cooldown, then hands out a real mining
	// job (paying the treasury) for the browser to solve as the captcha.
	mux.HandleFunc("/faucet/challenge", func(w http.ResponseWriter, r *http.Request) {
		addr := strings.TrimSpace(r.URL.Query().Get("addr"))
		if !core.ValidAddr(addr) {
			writeJSON(w, 400, map[string]string{"error": "enter a valid crb1 address"})
			return
		}
		if left := store.remaining(addr, clientIP(r), time.Now().Unix()); left > 0 {
			writeJSON(w, 429, map[string]string{"error": fmt.Sprintf("already claimed - try again in %dh %dm", int(left.Hours()), int(left.Minutes())%60)})
			return
		}
		id, fw, err := newChallenge()
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "faucet backend busy, try again"})
			return
		}
		writeJSON(w, 200, map[string]any{
			"challenge": id,
			"header":    hex.EncodeToString(fw.header),
			"target":    core.TargetToHex(shareTarget),
			"seed":      hex.EncodeToString(fw.seed),
			"height":    fw.height,
		})
	})
	mux.HandleFunc("/faucet/claim", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeJSON(w, 405, map[string]string{"error": "POST only"})
			return
		}
		var req struct {
			Addr      string `json:"addr"`
			Challenge string `json:"challenge"`
			Nonce     string `json:"nonce"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad request"})
			return
		}
		req.Addr = strings.TrimSpace(req.Addr)
		if !core.ValidAddr(req.Addr) {
			writeJSON(w, 400, map[string]string{"error": "enter a valid crb1 address"})
			return
		}
		nonce, perr := strconv.ParseUint(req.Nonce, 10, 64)
		if perr != nil {
			writeJSON(w, 400, map[string]string{"error": "bad nonce"})
			return
		}
		// consume the one-time challenge
		chMu.Lock()
		fw := challenges[req.Challenge]
		if fw != nil {
			delete(challenges, req.Challenge)
		}
		chMu.Unlock()
		if fw == nil || fw.exp < time.Now().Unix() {
			writeJSON(w, 400, map[string]string{"error": "challenge expired - refresh and mine again"})
			return
		}
		// verify the NeuroMorph share
		h := hashFor(fw, nonce)
		if !core.HashMeetsTarget(h, shareTarget) {
			writeJSON(w, 400, map[string]string{"error": "invalid share"})
			return
		}
		// jackpot: the share also meets the network target -> real block to treasury
		if core.HashMeetsTarget(h, fw.netTarget) {
			body, _ := json.Marshal(map[string]any{"id": fw.nodeID, "nonce": nonce})
			if resp, e := http.Post(nodeAPI+"/submitwork", "application/json", strings.NewReader(string(body))); e == nil {
				resp.Body.Close()
				log.Printf("faucet: share also solved BLOCK %d -> treasury", fw.height)
			}
		}
		// Route the captcha's real work into the pool: credit the captcha wallet a
		// share so it earns a steady slice of pool blocks (not just rare jackpots).
		if poolAPI != "" && creditSecret != "" {
			go creditPoolShare(captchaAddr, creditShares)
		}
		ip := clientIP(r)
		now := time.Now().Unix()
		// Atomically claim the cooldown slot BEFORE sending. Concurrent requests
		// for the same addr/IP now serialize: only the first reserves, the rest
		// get 429. Roll back if the payout itself fails.
		if left := store.reserve(req.Addr, ip, now); left > 0 {
			writeJSON(w, 429, map[string]string{"error": fmt.Sprintf("already claimed - try again in %dh %dm", int(left.Hours()), int(left.Minutes())%60)})
			return
		}
		amt, mined := amountFor(req.Addr)
		txid, err := sendCRB(req.Addr, amt)
		if err != nil {
			store.release(req.Addr, ip)
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"result": "sent", "txid": txid,
			"amount": float64(amt) / float64(core.CoinUnit), "mined": mined})
	})

	log.Fatal(http.ListenAndServe(*listen, mux))
}
