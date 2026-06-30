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
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"cereblix/core"
)

// unit is synapses per 1 CRB, identical to the CLI wallet.
const unit = float64(core.CoinUnit)

// formatCRB renders synapses as a plain 8-decimal CRB string (no unit suffix), so
// the frontend controls presentation. e.g. 1250000000 -> "12.50000000".
func formatCRB(v uint64) string { return fmt.Sprintf("%.8f", float64(v)/unit) }

// parseCRB parses a user CRB amount string into synapses (identical rounding to
// the CLI's toAmount).
func parseCRB(s string) (uint64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0, errors.New("bad amount")
	}
	return uint64(f*unit + 0.5), nil
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
	Txid string `json:"Txid"`
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
}

// NewApp constructs the App, loading the wallet store and node settings.
func NewApp() *App {
	return &App{
		store: mustLoadStore(),
		nodes: newNodeManager(),
	}
}

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

// startup is the Wails OnStartup hook; it captures the app context.
func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// ------------------------------------------------------------- wallet/keys

// WalletState reports whether a wallet exists, is encrypted, is locked, and how
// many addresses it holds (0 while locked, since the keys are sealed).
func (a *App) WalletState() WalletStateResult {
	exists, enc, locked, count := a.store.state()
	return WalletStateResult{Exists: exists, Encrypted: enc, Locked: locked, AddressCount: count}
}

// CreateWallet creates the first address; encrypts with passphrase if non-empty.
func (a *App) CreateWallet(passphrase string) (AddrResult, error) {
	e, err := a.store.create(passphrase)
	if err != nil {
		return AddrResult{}, err
	}
	return AddrResult{Label: e.Label, Addr: e.Addr}, nil
}

// Unlock decrypts an encrypted wallet. Returns false on a wrong passphrase.
func (a *App) Unlock(passphrase string) (bool, error) {
	return a.store.unlock(passphrase)
}

// Lock re-seals an encrypted wallet, wiping decrypted keys from memory.
func (a *App) Lock() { a.store.lock() }

// IsLocked reports whether the wallet is currently sealed.
func (a *App) IsLocked() bool { return a.store.isLocked() }

// CreateAddress adds a new labelled address.
func (a *App) CreateAddress(label string) (AddrResult, error) {
	e, err := a.store.add(label)
	if err != nil {
		return AddrResult{}, err
	}
	return AddrResult{Label: e.Label, Addr: e.Addr}, nil
}

// ListAddresses returns every address with its (best-effort) balance in CRB.
func (a *App) ListAddresses() ([]AddressBalance, error) {
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
	e, err := a.store.importKey(strings.TrimSpace(privHex), label)
	if err != nil {
		return AddrResult{}, err
	}
	return AddrResult{Label: e.Label, Addr: e.Addr}, nil
}

// ExportKey reveals a private key. This is the ONLY method that returns one; for
// an encrypted wallet the passphrase is re-verified even within an open session.
func (a *App) ExportKey(addrOrLabel, passphrase string) (string, error) {
	return a.store.exportKey(addrOrLabel, passphrase)
}

// EncryptWallet passphrase-encrypts a previously plaintext wallet.
func (a *App) EncryptWallet(passphrase string) error {
	return a.store.encrypt(passphrase)
}

// ChangePassphrase re-encrypts the wallet under a new passphrase.
func (a *App) ChangePassphrase(oldp, newp string) error {
	return a.store.changePassphrase(oldp, newp)
}

// ------------------------------------------------------------- send / RBF

// Send signs a payment locally and broadcasts it. An empty from auto-picks a
// funded address; an empty fee uses the node's suggested fee.
func (a *App) Send(fromAddrOrLabel, to, amountCRB, feeCRB string) (SendResult, error) {
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
	return a.rbfReplace(txid, feeCRB, false)
}

// Cancel voids a still-pending tx by replacing it with a 1-synapse self-send at
// the same nonce and a higher fee.
func (a *App) Cancel(txid, feeCRB string) (SendResult, error) {
	return a.rbfReplace(txid, feeCRB, true)
}

func (a *App) rbfReplace(txid, feeCRB string, cancel bool) (SendResult, error) {
	var loc core.TxLocation
	if err := a.nodes.get("/tx?id="+url.QueryEscape(strings.TrimSpace(txid)), &loc); err != nil {
		return SendResult{}, err
	}
	if loc.Coinbase {
		return SendResult{}, errors.New("cannot replace a coinbase transaction")
	}
	if !loc.Pending {
		return SendResult{}, errors.New("transaction is already confirmed - it can no longer be replaced")
	}
	from, err := a.store.find(loc.From)
	if err != nil {
		return SendResult{}, fmt.Errorf("sender %s is not in this wallet - only its owner can replace it", loc.From)
	}
	// Clear the node's replace-by-fee bar: old fee + 10% (at least +1 synapse).
	minFee := loc.Fee + loc.Fee/10
	if minFee <= loc.Fee {
		minFee = loc.Fee + 1
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
	to, amount := loc.To, loc.Amount
	if cancel {
		to, amount = from.Addr, 1 // tiny self-send voids the original
	}
	tx, err := a.buildSignedTx(from, to, amount, fee, loc.Nonce)
	if err != nil {
		return SendResult{}, err
	}
	return a.broadcast(tx)
}

// buildSignedTx assembles and locally signs a tx for the next block height.
func (a *App) buildSignedTx(from KeyEntry, to string, amount, fee, nonce uint64) (*core.Tx, error) {
	priv, err := hex.DecodeString(from.Priv)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("corrupt private key in wallet")
	}
	tx := &core.Tx{To: to, Amount: amount, Fee: fee, Nonce: nonce}
	core.SignTxAt(tx, ed25519.PrivateKey(priv), a.nextBlockHeight())
	return tx, nil
}

// broadcast posts a signed tx to the node and returns its txid.
func (a *App) broadcast(tx *core.Tx) (SendResult, error) {
	var out struct {
		TxID string `json:"txid"`
	}
	if err := a.nodes.post("/tx", tx, &out); err != nil {
		return SendResult{}, err
	}
	return SendResult{Txid: out.TxID}, nil
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

// suggestedFeeRaw fetches the node's self-adjusting fee (synapses), with a floor.
func (a *App) suggestedFeeRaw() uint64 {
	var s struct {
		Fee uint64 `json:"fee_suggested"`
	}
	if a.nodes.get("/status", &s) == nil && s.Fee > 0 {
		return s.Fee
	}
	return 1000 // 0.00001 CRB fallback floor
}

// nextBlockHeight returns tip+1, the height a new tx would be mined into (drives
// the ChainID-bound signing payload from ChainIDHeight on).
func (a *App) nextBlockHeight() uint64 {
	var s struct {
		Height uint64 `json:"height"`
	}
	if a.nodes.get("/status", &s) == nil {
		return s.Height + 1
	}
	return 0
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
	var txs []map[string]any
	if err := a.nodes.get("/mempool", &txs); err != nil {
		return nil, err
	}
	return txs, nil
}

// Search classifies a query as height/hash/txid/address; returns the node's JSON.
func (a *App) Search(q string) (map[string]any, error) {
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
	return a.nodes.setMode(mode, customURL)
}

// SetLockTimeout sets the auto-lock idle timeout in minutes (0 disables it). The
// frontend enforces the idle timer; this persists the user's choice.
func (a *App) SetLockTimeout(min int) error { return a.nodes.setLockTimeout(min) }

// NodeInfo probes the active node for reachability, height and sync state.
func (a *App) NodeInfo() NodeInfoResult { return a.nodes.nodeInfo() }

// StartFullNode switches to Full mode and starts the embedded in-process node.
func (a *App) StartFullNode() error { return a.nodes.startFull() }

// StopFullNode stops routing wallet traffic to the embedded node (switches back to
// Lite). The embedded node keeps running for the process lifetime.
func (a *App) StopFullNode() error { return a.nodes.stopFull() }
