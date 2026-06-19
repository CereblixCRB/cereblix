package main

import (
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cereblix/core"
)

// The submit ACCEPT path must do ZERO Postgres I/O in db-mode — a per-share DB write was the
// throughput-collapsing regression. We prove the fix STRUCTURALLY: drive the real submitworkHandler
// in db-mode with pg == nil. If the hot path still touched Postgres it would panic on the nil pool;
// a clean 200 "share" with the weight landing in the in-memory window proves it is DB-free.
func TestSubmitHotPathNoDB(t *testing.T) {
	freshState()
	pplnsN = 1 << 20
	vmSem = make(chan struct{}, 4)
	curParams, curParamEpoch = nil, ^uint64(0)
	enAssigned, enCounter = map[string]uint64{}, 0
	addrBuckets, addrSeen = map[string]*tbucket{}, map[string]time.Time{}
	perAddrRate, perAddrBurst = 1e6, 1e6
	poolAddr = "crb10000000000000000000000000000000000000000"

	dbMode = true // db-mode ON ...
	pg = nil      // ... but NO Postgres pool: any hot-path DB call would panic here
	poolActive.Store(true)
	defer func() { dbMode = false; poolActive.Store(false) }()

	miner := "crb1ac25dfff5a231631ef73a6c73df93ade1ad55c9e"
	en := extranonceFor(miner)   // first assignment → the tag we must echo in the nonce's top bits
	nonce := (en & 0xFFFF) << 48 // bind the nonce to our extranonce; low bits zero

	header := make([]byte, core.HeaderLen)
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	allOnes := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)) // 2^256-1
	workMu.Lock()
	curWork = &work{
		nodeID: "tip", header: header, seed: seed,
		height:      100,           // < DatasetHeight → fast hash (no 64 MiB dataset)
		netTarget:   big.NewInt(1), // impossible → the share is never a block
		shareTarget: allOnes,       // any hash ≤ 2^256-1 → the share is always accepted
		seen:        map[uint64]bool{},
	}
	workMu.Unlock()

	body := `{"id":"tip|` + miner + `","nonce":"` + strconv.FormatUint(nonce, 10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/submitwork", strings.NewReader(body))
	rr := httptest.NewRecorder()
	submitworkHandler(rr, req) // PANICS if the accept path dereferences the nil Postgres pool

	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"result":"share"`) {
		t.Fatalf("share not accepted (code=%d): %s", rr.Code, rr.Body.String())
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.PplnsSum != 1 {
		t.Fatalf("accepted share must add weight 1 to the in-memory window, got sum=%v", st.PplnsSum)
	}
}
