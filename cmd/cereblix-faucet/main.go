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

// amtTier maps a minimum lifetime captcha-solve count to a payout amount.
type amtTier struct {
	min int    // minimum prior solves (faucet payouts already received) to qualify
	amt uint64 // payout in base units
}

var (
	nodeAPI string
	tiers   []amtTier // payout tiers by solves, ascending by min
	priv    ed25519.PrivateKey
	from                        string // payout wallet (treasury), signs faucet payouts
	captchaAddr                 string // the captcha share mines to THIS wallet (separate)
	shareTarget                 *big.Int // fixed, easy target for the captcha share
	sendMu                      sync.Mutex
	nextNonce                   uint64
	store                       *limitStore
	poolAPI                     string  // if set, each solved captcha credits a pool share to captchaAddr
	creditSecret                string  // shared secret for the pool's /api/credit
	captchaWork                 uint64  // expected hashes per captcha share (= -work); reported to the pool for HONEST, work-proportional crediting
)

// ----------------------------------------------------------- rate-limit store

type limitStore struct {
	mu     sync.Mutex
	path   string
	Addr   map[string]int64 `json:"addr"`
	IP     map[string]int64 `json:"ip"`
	Solves map[string]int   `json:"solves"` // lifetime captcha-solve count per address (= payouts sent to it); NEVER pruned
}

func loadStore(path string) *limitStore {
	s := &limitStore{path: path, Addr: map[string]int64{}, IP: map[string]int64{}, Solves: map[string]int{}}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, s)
	}
	if s.Addr == nil {
		s.Addr = map[string]int64{}
	}
	if s.IP == nil {
		s.IP = map[string]int64{}
	}
	if s.Solves == nil {
		s.Solves = map[string]int{}
	}
	return s
}

// getSolves returns how many times this address has solved the captcha (= faucet
// payouts it has received from the treasury). incSolves records one more after a
// successful payout. The counter is the faucet's own payout ledger - exactly the
// count of treasury->address transactions, reliable past the node's 200-row
// /history cap and queried only AFTER the share is verified.
func (s *limitStore) getSolves(addr string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Solves[addr]
}
func (s *limitStore) incSolves(addr string) {
	s.mu.Lock()
	s.Solves[addr]++
	s.mu.Unlock()
	s.save()
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

// amountFor picks the payout tier from how many times the address has already
// solved the captcha (its prior faucet payouts). Returns (amount, prior solves).
func amountFor(addr string) (uint64, int) {
	n := store.getSolves(addr)
	amt := tiers[0].amt
	for _, t := range tiers {
		if n >= t.min {
			amt = t.amt
		}
	}
	return amt, n
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
	// Behind Cloudflare -> Caddy, the only trustworthy client IP is the one Cloudflare
	// stamps in CF-Connecting-IP. X-Forwarded-For's LAST hop is the Cloudflare/Caddy edge
	// (NOT the visitor), so keying cooldowns on it bucketed every user by edge IP: strangers
	// sharing an edge got a spurious "already claimed", and a free reclaim whenever the edge
	// rotated. CF-Connecting-IP is set by Cloudflare itself and cannot be spoofed by the client.
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	// No Cloudflare (direct/local): the FIRST X-Forwarded-For entry is the original client
	// recorded by the first proxy; each later hop is appended after it.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
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

// creditPoolShare tells the pool (over localhost) to credit the captcha wallet for the
// WORK actually done (expected hashes of the solved captcha share). The pool converts that
// to pool-shares at the same rate real miners earn them, so the captcha gets only its fair,
// work-proportional slice and never dilutes other miners. Best-effort: a failure never
// blocks the user's faucet claim.
func creditPoolShare(addr string) {
	c := &http.Client{Timeout: 6 * time.Second}
	u := fmt.Sprintf("%s/credit?addr=%s&work=%d", poolAPI, addr, captchaWork)
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
	t0 := flag.Float64("t0", 0.001, "CRB for a newcomer (0 captcha solves)")
	t100 := flag.Float64("t100", 0.003, "CRB at >=100 solves")
	t250 := flag.Float64("t250", 0.005, "CRB at >=250 solves")
	t500 := flag.Float64("t500", 0.007, "CRB at >=500 solves")
	t750 := flag.Float64("t750", 0.01, "CRB at >=750 solves (max)")
	work := flag.Uint64("work", 800, "captcha share difficulty in expected hashes (browser NeuroMorph)")
	captcha := flag.String("captcha-addr", "", "address the captcha share mines to (defaults to payout wallet)")
	datadir := flag.String("datadir", "/var/lib/cerebra", "where to store rate-limit state")
	pool := flag.String("pool", "", "pool API base; if set, each solved captcha credits a pool share to the captcha wallet")
	creditSecretFile := flag.String("credit-secret-file", "", "file with the shared secret for the pool's /api/credit")
	flag.Parse()

	poolAPI = strings.TrimRight(*pool, "/")
	captchaWork = *work
	if *creditSecretFile != "" {
		if b, err := os.ReadFile(*creditSecretFile); err == nil {
			creditSecret = strings.TrimSpace(string(b))
		}
	}

	toBase := func(v float64) uint64 { return uint64(v * float64(core.CoinUnit)) }
	tiers = []amtTier{{0, toBase(*t0)}, {100, toBase(*t100)}, {250, toBase(*t250)}, {500, toBase(*t500)}, {750, toBase(*t750)}}
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
	log.Printf("faucet: payout %s, captcha mines to %s, tiers(solves 0/100/250/500/750) = %d/%d/%d/%d/%d base, 3h cooldown, work=%d, listen %s",
		from, captchaAddr, tiers[0].amt, tiers[1].amt, tiers[2].amt, tiers[3].amt, tiers[4].amt, *work, *listen)

	mux := http.NewServeMux()
	mux.HandleFunc("/faucet/info", func(w http.ResponseWriter, r *http.Request) {
		ts := make([]map[string]any, len(tiers))
		for i, t := range tiers {
			ts[i] = map[string]any{"min": t.min, "amount": float64(t.amt) / float64(core.CoinUnit)}
		}
		writeJSON(w, 200, map[string]any{"from": from, "cooldown_h": 3, "tiers": ts})
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
			go creditPoolShare(captchaAddr)
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
		amt, solves := amountFor(req.Addr)
		txid, err := sendCRB(req.Addr, amt)
		if err != nil {
			store.release(req.Addr, ip)
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		store.incSolves(req.Addr) // this successful claim counts toward the next tier
		writeJSON(w, 200, map[string]any{"result": "sent", "txid": txid,
			"amount": float64(amt) / float64(core.CoinUnit), "solves": solves + 1})
	})

	log.Fatal(http.ListenAndServe(*listen, mux))
}
