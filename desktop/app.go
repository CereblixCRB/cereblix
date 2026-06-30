package main

// app.go is the Wails binding surface: the App struct and every method the
// frontend calls as window.go.main.App.MethodName(...). It owns the local key
// store (store.go) and the node manager (node_modes.go), reuses cereblix/core for
// address/amount rules and local ed25519 signing, and broadcasts via the node RPC.
//
// SECURITY: private keys live only in the Store and are used in-process for
// signing. The ONLY method that returns a private key is ExportKey.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cereblix/core"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// unit is synapses per 1 CRB, identical to the CLI wallet.
const unit = float64(core.CoinUnit)

// formatCRB renders synapses as a plain 8-decimal CRB string (no unit suffix), so
// the frontend controls presentation. e.g. 1250000000 -> "12.50000000".
func formatCRB(v uint64) string { return fmt.Sprintf("%.8f", float64(v)/unit) }

// parseCRB parses a user CRB amount string into integer synapses WITHOUT going
// through float64, so a value like 0.05 maps to exactly 5000000 synapses with no
// binary-floating-point rounding error. It rejects anything that is not a plain
// non-negative decimal with at most 8 fractional digits.
func parseCRB(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty amount")
	}
	// No sign, no exponent, no NaN/Inf: only digits and a single dot are allowed.
	if strings.ContainsAny(s, "+-") {
		return 0, errors.New("bad amount")
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return 0, errors.New("bad amount: more than one decimal point")
	}
	intPart := parts[0]
	fracPart := ""
	if len(parts) == 2 {
		fracPart = parts[1]
	}
	if intPart == "" && fracPart == "" {
		return 0, errors.New("bad amount")
	}
	if !allDigits(intPart) || !allDigits(fracPart) {
		return 0, errors.New("bad amount")
	}
	if len(fracPart) > 8 {
		return 0, errors.New("max 8 decimals")
	}
	for len(fracPart) < 8 { // pad the fractional part to exactly 8 digits (synapses)
		fracPart += "0"
	}
	var whole uint64
	if intPart != "" {
		w, err := strconv.ParseUint(intPart, 10, 64)
		if err != nil {
			return 0, errors.New("amount too large")
		}
		whole = w
	}
	var frac uint64
	if fracPart != "" {
		f, err := strconv.ParseUint(fracPart, 10, 64)
		if err != nil {
			return 0, errors.New("bad amount")
		}
		frac = f
	}
	const unitInt = uint64(core.CoinUnit)
	// Overflow-safe: whole*unitInt + frac must fit in uint64.
	if whole > (^uint64(0)-frac)/unitInt {
		return 0, errors.New("amount too large")
	}
	return whole*unitInt + frac, nil
}

// allDigits reports whether every byte of s is an ASCII digit. The empty string
// is considered all-digits (callers handle the empty intPart/fracPart cases).
func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------- DTOs
// Result types returned to the frontend (auto-marshaled to JS objects by Wails).

type WalletStateResult struct {
	Exists       bool `json:"Exists"`
	Encrypted    bool `json:"Encrypted"`
	Locked       bool `json:"Locked"`
	AddressCount int  `json:"AddressCount"`
}

type AddrResult struct {
	Label string `json:"Label"`
	Addr  string `json:"Addr"`
}

type AddressBalance struct {
	Label   string `json:"Label"`
	Addr    string `json:"Addr"`
	Balance string `json:"Balance"` // pre-formatted CRB (8 decimals)
}

type SendResult struct {
	Txid   string `json:"Txid"`
	To     string `json:"To"`     // resolved destination (trusted local value for RBF)
	Amount string `json:"Amount"` // resolved amount, pre-formatted CRB
}

// UpdateInfo is the result of CheckUpdate. Available is false unless a manifest
// was fetched, signature-verified against the pinned release key, and found to
// advertise a strictly-newer version.
type UpdateInfo struct {
	Available bool   `json:"Available"`
	Version   string `json:"Version"`
	Notes     string `json:"Notes"`
	URL       string `json:"URL"`
	Sha256    string `json:"Sha256"`
}

type SettingsResult struct {
	NodeMode       string   `json:"NodeMode"`
	Endpoint       string   `json:"Endpoint"`
	Endpoints      []string `json:"Endpoints"`
	LockTimeoutMin int      `json:"LockTimeoutMin"`
}

type NodeInfoResult struct {
	Mode       string `json:"Mode"`
	Endpoint   string `json:"Endpoint"`
	Reachable  bool   `json:"Reachable"`
	Syncing    bool   `json:"Syncing"`
	Height     uint64 `json:"Height"`
	SyncHeight uint64 `json:"SyncHeight"`
}

// ------------------------------------------------------------------- App

type App struct {
	ctx   context.Context
	store *Store
	nodes *NodeManager
	sent  *sentLog // local record of txns this wallet broadcast (RBF safety)

	lastActivity atomic.Int64 // unix seconds of the last user-driven binding call
}

// NewApp constructs the App, loading the wallet store and node settings.
func NewApp() *App {
	a := &App{
		store: mustLoadStore(),
		nodes: newNodeManager(),
		sent:  loadSentLog(sentLogPath()),
	}
	a.lastActivity.Store(time.Now().Unix())
	return a
}

// touch records user activity for the backend idle-lock fail-safe.
func (a *App) touch() { a.lastActivity.Store(time.Now().Unix()) }

// mustLoadStore opens the default wallet, degrading gracefully on a load error to
// an "exists" placeholder that will NOT overwrite a possibly-corrupt file.
func mustLoadStore() *Store {
	path := defaultWalletPath()
	s, err := loadStore(path)
	if err != nil {
		log.Printf("wallet load warning (%s): %v", path, err)
		return &Store{path: path, exists: true}
	}
	return s
}

// startup is the Wails OnStartup hook; it captures the app context and launches
// the backend idle-lock fail-safe.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.touch()
	a.startAutoLock()
}

// startAutoLock runs a backend-enforced idle lock independent of the frontend: it
// wipes the decrypted keys from memory once the wallet has been idle past the
// configured timeout, even if the renderer's own timer never fires (frozen page,
// suspended JS, a tampered frontend). Reuses the existing lock-timeout setting.
func (a *App) startAutoLock() {
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			timeoutMin := a.nodes.lockTimeout()
			if timeoutMin <= 0 {
				continue // auto-lock disabled
			}
			if !a.store.isEncrypted() || a.store.isLocked() {
				continue // nothing to seal
			}
			last := time.Unix(a.lastActivity.Load(), 0)
			if time.Since(last) >= time.Duration(timeoutMin)*time.Minute {
				a.store.lock()
			}
		}
	}()
}

// ------------------------------------------------------------- wallet/keys

// WalletState reports whether a wallet exists, is encrypted, is locked, and how
// many addresses it holds (0 while locked, since the keys are sealed).
func (a *App) WalletState() WalletStateResult {
	exists, enc, locked, count := a.store.state()
	return WalletStateResult{Exists: exists, Encrypted: enc, Locked: locked, AddressCount: count}
}

// CreateWallet creates the first address, encrypting the wallet with passphrase.
func (a *App) CreateWallet(passphrase string) (AddrResult, error) {
	a.touch()
	e, err := a.store.create(passphrase)
	if err != nil {
		return AddrResult{}, err
	}
	return AddrResult{Label: e.Label, Addr: e.Addr}, nil
}

// Unlock decrypts an encrypted wallet. Returns false on a wrong passphrase.
func (a *App) Unlock(passphrase string) (bool, error) {
	a.touch()
	return a.store.unlock(passphrase)
}

// Lock re-seals an encrypted wallet, wiping decrypted keys from memory.
func (a *App) Lock() { a.store.lock() }

// IsLocked reports whether the wallet is currently sealed.
func (a *App) IsLocked() bool { return a.store.isLocked() }

// CreateAddress adds a new labelled address.
func (a *App) CreateAddress(label string) (AddrResult, error) {
	a.touch()
	e, err := a.store.add(label)
	if err != nil {
		return AddrResult{}, err
	}
	return AddrResult{Label: e.Label, Addr: e.Addr}, nil
}

// ListAddresses returns every address with its (best-effort) balance in CRB.
func (a *App) ListAddresses() ([]AddressBalance, error) {
	a.touch()
	keys, err := a.store.list()
	if err != nil {
		return nil, err
	}
	out := make([]AddressBalance, 0, len(keys))
	for _, k := range keys {
		var b struct {
			Balance uint64 `json:"balance"`
		}
		_ = a.nodes.get("/balance?addr="+url.QueryEscape(k.Addr), &b) // best-effort
		out = append(out, AddressBalance{Label: k.Label, Addr: k.Addr, Balance: formatCRB(b.Balance)})
	}
	return out, nil
}

// TotalBalance sums every address's balance and returns it in CRB.
func (a *App) TotalBalance() (string, error) {
	a.touch()
	keys, err := a.store.list()
	if err != nil {
		return "", err
	}
	var total uint64
	for _, k := range keys {
		var b struct {
			Balance uint64 `json:"balance"`
		}
		_ = a.nodes.get("/balance?addr="+url.QueryEscape(k.Addr), &b)
		total += b.Balance
	}
	return formatCRB(total), nil
}

// ImportKey adds an existing 128-hex ed25519 private key under label.
func (a *App) ImportKey(privHex, label string) (AddrResult, error) {
	a.touch()
	e, err := a.store.importKey(strings.TrimSpace(privHex), label)
	if err != nil {
		return AddrResult{}, err
	}
	return AddrResult{Label: e.Label, Addr: e.Addr}, nil
}

// ExportKey reveals a private key. This is the ONLY method that returns one; for
// an encrypted wallet the passphrase is re-verified even within an open session.
func (a *App) ExportKey(addrOrLabel, passphrase string) (string, error) {
	a.touch()
	return a.store.exportKey(addrOrLabel, passphrase)
}

// EncryptWallet passphrase-encrypts a previously plaintext wallet.
func (a *App) EncryptWallet(passphrase string) error {
	a.touch()
	return a.store.encrypt(passphrase)
}

// ChangePassphrase re-encrypts the wallet under a new passphrase.
func (a *App) ChangePassphrase(oldp, newp string) error {
	a.touch()
	return a.store.changePassphrase(oldp, newp)
}

// ------------------------------------------------------------- send / RBF

// Send signs a payment locally and broadcasts it. An empty from auto-picks a
// funded address; an empty fee uses the node's suggested fee.
func (a *App) Send(fromAddrOrLabel, to, amountCRB, feeCRB string) (SendResult, error) {
	a.touch()
	to = strings.TrimSpace(to)
	if !core.ValidAddr(to) {
		return SendResult{}, errors.New("invalid destination address")
	}
	amount, err := parseCRB(amountCRB)
	if err != nil {
		return SendResult{}, err
	}
	if amount == 0 {
		return SendResult{}, errors.New("amount must be greater than zero")
	}
	fee, err := a.resolveFee(feeCRB)
	if err != nil {
		return SendResult{}, err
	}

	var from KeyEntry
	if strings.TrimSpace(fromAddrOrLabel) == "" {
		from, err = a.pickFundedAddr(amount + fee)
	} else {
		from, err = a.store.find(fromAddrOrLabel)
	}
	if err != nil {
		return SendResult{}, err
	}

	nonce, err := a.accountNonce(from.Addr)
	if err != nil {
		return SendResult{}, err
	}
	tx, err := a.buildSignedTx(from, to, amount, fee, nonce)
	if err != nil {
		return SendResult{}, err
	}
	return a.broadcast(tx)
}

// SpeedUp re-broadcasts a still-pending tx at a higher fee so it confirms sooner.
func (a *App) SpeedUp(txid, feeCRB string) (SendResult, error) {
	a.touch()
	return a.rbfReplace(txid, feeCRB, false)
}

// Cancel voids a still-pending tx by replacing it with a 1-synapse self-send at
// the same nonce and a higher fee.
func (a *App) Cancel(txid, feeCRB string) (SendResult, error) {
	a.touch()
	return a.rbfReplace(txid, feeCRB, true)
}

// rbfReplace builds a replace-by-fee transaction. For a Speed up (cancel=false) it
// MUST re-sign the ORIGINAL payment, so it resolves the destination/amount/nonce
// from THIS wallet's own local record of what it broadcast - NEVER from the node.
// A malicious node could otherwise return a rewritten /tx (different `to`) and a
// single "Speed up" click would redirect the funds. For a Cancel it forces a tiny
// self-send, so even a lying node cannot move funds anywhere.
func (a *App) rbfReplace(txid, feeCRB string, cancel bool) (SendResult, error) {
	txid = strings.TrimSpace(txid)
	var loc core.TxLocation
	if err := a.nodes.get("/tx?id="+url.QueryEscape(txid), &loc); err != nil {
		return SendResult{}, err
	}
	if loc.Coinbase {
		return SendResult{}, errors.New("cannot replace a coinbase transaction")
	}
	if !loc.Pending {
		return SendResult{}, errors.New("transaction is already confirmed - it can no longer be replaced")
	}

	rec, haveRec := a.sent.find(txid)

	var from KeyEntry
	var to string
	var amount, nonce, oldFee uint64

	if cancel {
		// Cancel: a 1-synapse self-send at the same nonce voids the original. The
		// sender must be in this wallet; the destination is forced to our own
		// address. Prefer our trusted local record for nonce/old-fee when present.
		f, err := a.store.find(loc.From)
		if err != nil {
			return SendResult{}, fmt.Errorf("sender %s is not in this wallet - only its owner can replace it", loc.From)
		}
		from, to, amount, nonce, oldFee = f, f.Addr, 1, loc.Nonce, loc.Fee
		if haveRec {
			nonce, oldFee = rec.Nonce, rec.Fee
		}
	} else {
		// Speed up: re-sign the SAME payment. Refuse unless we hold our own record
		// of it - we will not trust the node for the destination, amount or nonce.
		if !haveRec {
			return SendResult{}, errors.New("can only speed up transactions sent from this wallet")
		}
		f, err := a.store.find(rec.From)
		if err != nil {
			return SendResult{}, fmt.Errorf("sender %s is not in this wallet - only its owner can replace it", rec.From)
		}
		from, to, amount, nonce, oldFee = f, rec.To, rec.Amount, rec.Nonce, rec.Fee
	}

	// Clear the node's replace-by-fee bar: old fee + 10% (at least +1 synapse).
	minFee := oldFee + oldFee/10
	if minFee <= oldFee {
		minFee = oldFee + 1
	}
	fee := minFee
	if s := a.suggestedFeeRaw(); s > fee {
		fee = s
	}
	if strings.TrimSpace(feeCRB) != "" {
		f, err := parseCRB(feeCRB)
		if err != nil {
			return SendResult{}, err
		}
		if f < minFee {
			return SendResult{}, fmt.Errorf("fee too low: need >= %s CRB to replace (old fee + 10%%)", formatCRB(minFee))
		}
		fee = f
	}

	tx, err := a.buildSignedTx(from, to, amount, fee, nonce)
	if err != nil {
		return SendResult{}, err
	}
	return a.broadcast(tx)
}

// buildSignedTx assembles and locally signs a tx for the next block height. It
// FAILS if the current tip height cannot be read from a reachable node, so a tx is
// never signed at height 0 (which would use the replayable pre-ChainID payload).
func (a *App) buildSignedTx(from KeyEntry, to string, amount, fee, nonce uint64) (*core.Tx, error) {
	priv, err := hex.DecodeString(from.Priv)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("corrupt private key in wallet")
	}
	height, err := a.nextBlockHeight()
	if err != nil {
		return nil, err
	}
	tx := &core.Tx{To: to, Amount: amount, Fee: fee, Nonce: nonce}
	core.SignTxAt(tx, ed25519.PrivateKey(priv), height)
	return tx, nil
}

// broadcast posts a signed tx to the node, records it locally so a later "Speed
// up" can re-sign the same payment from trusted data, and returns its txid plus
// the resolved destination/amount.
func (a *App) broadcast(tx *core.Tx) (SendResult, error) {
	var out struct {
		TxID string `json:"txid"`
	}
	if err := a.nodes.post("/tx", tx, &out); err != nil {
		return SendResult{}, err
	}
	// The txid is locally derived from the fully-signed tx (deterministic), so it
	// is trustworthy even if the node claims a different id.
	txid := tx.ID()
	from, _ := tx.FromAddr()
	a.sent.add(SentRecord{
		Txid:   txid,
		From:   from,
		To:     tx.To,
		Amount: tx.Amount,
		Nonce:  tx.Nonce,
		Fee:    tx.Fee,
		Time:   time.Now().Unix(),
	})
	return SendResult{Txid: txid, To: tx.To, Amount: formatCRB(tx.Amount)}, nil
}

// pickFundedAddr returns the first wallet address whose balance covers need.
func (a *App) pickFundedAddr(need uint64) (KeyEntry, error) {
	keys, err := a.store.list()
	if err != nil {
		return KeyEntry{}, err
	}
	for _, k := range keys {
		var b struct {
			Balance uint64 `json:"balance"`
		}
		if a.nodes.get("/balance?addr="+url.QueryEscape(k.Addr), &b) == nil && b.Balance >= need {
			return a.store.find(k.Addr) // re-fetch WITH private key for signing
		}
	}
	return KeyEntry{}, errors.New("no single address has enough balance (specify one or top up)")
}

// resolveFee returns the requested fee in synapses, or the node suggestion when
// the input is empty.
func (a *App) resolveFee(feeCRB string) (uint64, error) {
	if strings.TrimSpace(feeCRB) == "" {
		return a.suggestedFeeRaw(), nil
	}
	return parseCRB(feeCRB)
}

func (a *App) accountNonce(addr string) (uint64, error) {
	var acc struct {
		Nonce uint64 `json:"nonce"`
	}
	if err := a.nodes.get("/balance?addr="+url.QueryEscape(addr), &acc); err != nil {
		return 0, err
	}
	return acc.Nonce, nil
}

// maxAutoFee caps the node-suggested ("auto") fee so a malicious node cannot make
// a blank-fee send pay an absurd amount. 1 CRB is ~100,000x the 0.00001 CRB floor
// and far above any sane congestion fee, so a legitimate suggestion is never
// clamped while an attacker's 10,000-CRB "suggestion" is.
const maxAutoFee = uint64(core.CoinUnit) // 1 CRB

// suggestedFeeRaw fetches the node's self-adjusting fee (synapses), with a floor
// and a hard sanity cap (see maxAutoFee).
func (a *App) suggestedFeeRaw() uint64 {
	var s struct {
		Fee uint64 `json:"fee_suggested"`
	}
	if a.nodes.get("/status", &s) == nil && s.Fee > 0 {
		if s.Fee > maxAutoFee {
			return maxAutoFee
		}
		return s.Fee
	}
	return 1000 // 0.00001 CRB fallback floor
}

// nextBlockHeight returns tip+1, the height a new tx would be mined into (drives
// the ChainID-bound signing payload from ChainIDHeight on). It returns an error if
// the tip cannot be read, so the send path never signs at height 0 (a replayable,
// pre-ChainID payload).
func (a *App) nextBlockHeight() (uint64, error) {
	var s struct {
		Height uint64 `json:"height"`
	}
	if err := a.nodes.get("/status", &s); err != nil {
		return 0, fmt.Errorf("cannot reach a node to read the current block height (try again when online): %w", err)
	}
	return s.Height + 1, nil
}

// SuggestedFee returns the node's suggested fee, formatted in CRB.
func (a *App) SuggestedFee() (string, error) {
	return formatCRB(a.suggestedFeeRaw()), nil
}

// ValidateAddress reports whether addr is a well-formed crb1 address.
func (a *App) ValidateAddress(addr string) bool {
	return core.ValidAddr(strings.TrimSpace(addr))
}

// History returns recent transactions for one address/label, or (empty arg) the
// merged, newest-first history across all wallet addresses.
func (a *App) History(addrOrLabel string) ([]core.HistoryItem, error) {
	a.touch()
	var addrs []string
	if strings.TrimSpace(addrOrLabel) == "" {
		keys, err := a.store.list()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			addrs = append(addrs, k.Addr)
		}
	} else {
		addrs = append(addrs, a.resolveAddr(addrOrLabel))
	}
	if len(addrs) == 0 {
		return []core.HistoryItem{}, nil
	}
	seen := map[string]bool{}
	var all []core.HistoryItem
	for _, addr := range addrs {
		var hist []core.HistoryItem
		if err := a.nodes.get("/history?addr="+url.QueryEscape(addr)+"&limit=20", &hist); err != nil {
			return nil, err
		}
		for _, h := range hist {
			if seen[h.TxID] {
				continue
			}
			seen[h.TxID] = true
			all = append(all, h)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Height > all[j].Height })
	return all, nil
}

// resolveAddr maps a wallet label to its address; an unknown/raw value passes
// through. Never errors (a locked wallet simply can't resolve labels).
func (a *App) resolveAddr(s string) string {
	if e, err := a.store.find(s); err == nil {
		return e.Addr
	}
	return strings.TrimSpace(s)
}

// ------------------------------------------------------------- explorer

// NetworkStatus returns the chain's headline status as the node's RAW JSON
// (height, tip, supply, hashrate, difficulty, reward, mempool, peers, epoch,
// fee_suggested, node_version, consensus_version, ...). Passing the node's own
// lowercase fields straight through keeps the frontend reading one shape in both
// dev (mock) and production, and never silently drops a field the UI shows.
func (a *App) NetworkStatus() (map[string]any, error) {
	var s map[string]any
	if err := a.nodes.get("/status", &s); err != nil {
		return nil, err
	}
	return s, nil
}

// GetBlock looks up a block by height or 64-hex hash; returns the node's JSON.
func (a *App) GetBlock(q string) (map[string]any, error) {
	a.touch()
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, errors.New("empty block query")
	}
	query := "h=" + url.QueryEscape(q)
	if len(q) == 64 {
		query = "hash=" + url.QueryEscape(q)
	}
	var b map[string]any
	if err := a.nodes.get("/block?"+query, &b); err != nil {
		return nil, err
	}
	return b, nil
}

// GetTx looks up a transaction by id (confirmed or pending).
func (a *App) GetTx(txid string) (core.TxLocation, error) {
	a.touch()
	var loc core.TxLocation
	if err := a.nodes.get("/tx?id="+url.QueryEscape(strings.TrimSpace(txid)), &loc); err != nil {
		return core.TxLocation{}, err
	}
	return loc, nil
}

// AddressInfo returns the node's RAW /balance JSON for any address (address,
// balance, spendable, nonce, received, mined, sent, txn) so the explorer renders
// every field the node exposes. Amounts stay in synapses; the frontend formats.
func (a *App) AddressInfo(addr string) (map[string]any, error) {
	a.touch()
	addr = a.resolveAddr(addr)
	if !core.ValidAddr(addr) {
		return nil, errors.New("invalid address")
	}
	var b map[string]any
	if err := a.nodes.get("/balance?addr="+url.QueryEscape(addr), &b); err != nil {
		return nil, err
	}
	return b, nil
}

// Richlist returns the top n addresses by balance.
func (a *App) Richlist(n int) ([]core.RichEntry, error) {
	a.touch()
	if n <= 0 {
		n = 25
	}
	var list []core.RichEntry
	if err := a.nodes.get(fmt.Sprintf("/richlist?n=%d", n), &list); err != nil {
		return nil, err
	}
	return list, nil
}

// Mempool returns the unconfirmed transactions as the node's RAW JSON
// ([]{from_pub,to,amount,fee,nonce,sig}); the frontend treats an empty from_pub
// as coinbase and formats amounts itself — matching the block/tx views.
func (a *App) Mempool() ([]map[string]any, error) {
	a.touch()
	var txs []map[string]any
	if err := a.nodes.get("/mempool", &txs); err != nil {
		return nil, err
	}
	return txs, nil
}

// Search classifies a query as height/hash/txid/address; returns the node's JSON.
func (a *App) Search(q string) (map[string]any, error) {
	a.touch()
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, errors.New("empty search query")
	}
	var res map[string]any
	if err := a.nodes.get("/search?q="+url.QueryEscape(q), &res); err != nil {
		return nil, err
	}
	return res, nil
}

// ------------------------------------------------------------- node modes

// GetSettings returns the current node mode, active endpoint, Lite endpoint list,
// and the auto-lock timeout.
func (a *App) GetSettings() SettingsResult { return a.nodes.settings() }

// SetNodeMode switches the node mode ("lite" | "full" | "custom"). Custom requires
// a node URL; Full starts the embedded node.
func (a *App) SetNodeMode(mode, customURL string) error {
	a.touch()
	return a.nodes.setMode(mode, customURL)
}

// SetLockTimeout sets the auto-lock idle timeout in minutes (0 disables it). The
// frontend enforces the idle timer; this persists the user's choice.
func (a *App) SetLockTimeout(min int) error { a.touch(); return a.nodes.setLockTimeout(min) }

// NodeInfo probes the active node for reachability, height and sync state.
func (a *App) NodeInfo() NodeInfoResult { return a.nodes.nodeInfo() }

// StartFullNode switches to Full mode and starts the embedded in-process node.
func (a *App) StartFullNode() error { a.touch(); return a.nodes.startFull() }

// StopFullNode stops routing wallet traffic to the embedded node (switches back to
// Lite). The embedded node keeps running for the process lifetime.
func (a *App) StopFullNode() error { a.touch(); return a.nodes.stopFull() }

// ------------------------------------------------- sent-tx record (RBF safety)

// SentRecord is a locally-persisted note of a payment THIS wallet broadcast, kept
// so a later "Speed up" re-signs the SAME payment from trusted local values
// instead of values fetched from a (possibly malicious) node.
type SentRecord struct {
	Txid   string `json:"txid"`
	From   string `json:"from"`
	To     string `json:"to"`
	Amount uint64 `json:"amount"`
	Nonce  uint64 `json:"nonce"`
	Fee    uint64 `json:"fee"`
	Time   int64  `json:"time"`
}

// sentLog is the atomically-persisted set of SentRecords at
// <userhome>\.cereblix\wallet-sent.json. Safe for concurrent use.
type sentLog struct {
	mu   sync.Mutex
	path string
	recs []SentRecord
}

func sentLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".cereblix", "wallet-sent.json")
}

// loadSentLog reads the sent-tx record on startup. A missing or corrupt file is
// treated as empty (the record is a convenience cache, not authoritative state).
func loadSentLog(path string) *sentLog {
	l := &sentLog{path: path}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &l.recs)
	}
	return l
}

// add appends (or refreshes) a record and persists the log atomically.
func (l *sentLog) add(r SentRecord) {
	if r.Txid == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.recs {
		if l.recs[i].Txid == r.Txid {
			l.recs[i] = r
			l.save()
			return
		}
	}
	l.recs = append(l.recs, r)
	const maxRecs = 1000 // bound the file so it cannot grow without limit
	if len(l.recs) > maxRecs {
		l.recs = l.recs[len(l.recs)-maxRecs:]
	}
	l.save()
}

// find returns the stored record for txid, if any.
func (l *sentLog) find(txid string) (SentRecord, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.recs {
		if l.recs[i].Txid == txid {
			return l.recs[i], true
		}
	}
	return SentRecord{}, false
}

// save writes the log atomically with 0600 perms. Caller holds l.mu. Failures are
// non-fatal (the record only affects the "Speed up" convenience path).
func (l *sentLog) save() {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return
	}
	raw, err := json.MarshalIndent(l.recs, "", "  ")
	if err != nil {
		return
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, l.path)
}

// ------------------------------------------------------ signed update check

const (
	// AppVersion is this wallet build's version (semver). Compared against the
	// signed manifest to decide whether a newer release is available.
	AppVersion = "1.0.0"

	// walletReleasePubHex is the ed25519 public key (hex) that signs the wallet
	// update manifest. A manifest that does not verify against this pinned key is
	// ignored entirely, so a forged/unsigned manifest can never advertise an update.
	walletReleasePubHex = "de9a9336d692524da0c248a5de8fb01d4f88487d1411868320a3e3ea1be0d32d"

	// updateManifestURL is the signed release manifest. updatePlatform selects the
	// download entry for this build's OS/arch.
	updateManifestURL = "https://cereblix.com/wallet/latest.json"
	updatePlatform    = "windows-amd64"
)

// CheckUpdate fetches the signed release manifest, verifies its ed25519 signature
// against the pinned release key, and reports whether a strictly-newer version is
// available. ANY failure (offline, bad JSON, bad/forged signature) returns
// {Available:false} - an unsigned or tampered manifest is never trusted.
func (a *App) CheckUpdate() (UpdateInfo, error) {
	a.touch()
	none := UpdateInfo{Available: false}

	pub, err := hex.DecodeString(walletReleasePubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return none, nil // misconfigured pin -> trust nothing
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirects are not allowed for the update manifest")
		},
	}
	resp, err := client.Get(updateManifestURL)
	if err != nil {
		return none, nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil || resp.StatusCode != http.StatusOK {
		return none, nil
	}

	var env struct {
		Payload string `json:"payload"`
		Sig     string `json:"sig"`
	}
	if json.Unmarshal(body, &env) != nil || env.Payload == "" || env.Sig == "" {
		return none, nil
	}
	sig, err := hex.DecodeString(env.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return none, nil
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(env.Payload), sig) {
		return none, nil // forged / unsigned -> ignore
	}

	var payload struct {
		Version   string `json:"version"`
		Notes     string `json:"notes"`
		Platforms map[string]struct {
			URL    string `json:"url"`
			Sha256 string `json:"sha256"`
		} `json:"platforms"`
	}
	if json.Unmarshal([]byte(env.Payload), &payload) != nil || payload.Version == "" {
		return none, nil
	}
	if !semverNewer(payload.Version, AppVersion) {
		return none, nil
	}
	plat := payload.Platforms[updatePlatform]
	return UpdateInfo{
		Available: true,
		Version:   payload.Version,
		Notes:     payload.Notes,
		URL:       plat.URL,
		Sha256:    plat.Sha256,
	}, nil
}

// OpenExternal opens an https URL in the SYSTEM browser (never the webview), so a
// download link can never be loaded inside the wallet's renderer.
func (a *App) OpenExternal(rawURL string) error {
	a.touch()
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("refusing to open a non-https URL")
	}
	if a.ctx != nil {
		runtime.BrowserOpenURL(a.ctx, rawURL)
	}
	return nil
}

// semverNewer reports whether semver string a is strictly newer than b.
func semverNewer(a, b string) bool {
	pa, pb := parseSemver(a), parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

// parseSemver extracts the numeric major.minor.patch from a version string,
// tolerating a leading "v" and any "-pre"/"+build" suffix.
func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, p := range strings.Split(v, ".") {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(p)
		out[i] = n
	}
	return out
}
