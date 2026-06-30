/* ============================================================================
   mock.js — DEV-ONLY in-memory backend.
   Installs window.go ONLY when the real Wails bindings are absent (e.g. opening
   index.html directly in a browser for design review). Inside the packaged
   Wails app window.go already exists, so this file is inert. Shapes mirror the
   real binding contract exactly (capitalized wallet structs, json-tag-lowercase
   core/explorer structs) so what you see here matches production.
   ========================================================================== */
(function (global) {
  "use strict";
  // Never shadow the real backend: bail if the Wails bindings exist, OR if the
  // Wails runtime is present (bindings may still be injecting). The mock only
  // activates in a plain browser (design preview), where none of these exist.
  if (global.go && global.go.main && global.go.main.App) return;
  if (global.runtime || global.wails || global.WailsInvoke) return;

  function hex(n) { var s = ""; while (s.length < n) s += "0123456789abcdef"[(Math.random() * 16) | 0]; return s; }
  function addr() { return "crb1" + hex(40); }
  var UNIT = 100000000;
  function crb(n) { return (n / UNIT).toFixed(8); }

  var W = {
    exists: false, encrypted: false, locked: false, pass: "",
    keys: [], // {Label, Addr, _bal, _nonce}
    settings: { NodeMode: "lite", Endpoint: "https://cereblix.com/api", Endpoints: ["https://cereblix.com/api", "https://ru.cereblix.com/api"], LockTimeoutMin: 10 },
    height: 7421
  };
  function newKey(label, bal) { return { Label: label, Addr: addr(), _priv: hex(128), _bal: bal || 0, _nonce: 0 }; }
  function find(q) { return W.keys.filter(function (k) { return k.Addr === q || k.Label === q; })[0]; }
  function ms(v) { return new Promise(function (r) { setTimeout(function () { r(v); }, 140 + Math.random() * 160); }); }
  function fail(msg) { return new Promise(function (_, rej) { setTimeout(function () { rej(new Error(msg)); }, 160); }); }

  var fakeHist = [];
  function seedHistory() {
    if (!W.keys.length) return;
    var me = W.keys[0].Addr, now = Math.floor(Date.now() / 1000);
    fakeHist = [
      { txid: hex(64), height: 7420, time: now - 400, from: "coinbase", to: me, amount: 50 * UNIT, fee: 0 },
      { txid: hex(64), height: 7411, time: now - 5400, from: me, to: addr(), amount: 12.5 * UNIT, fee: 0.001 * UNIT },
      { txid: hex(64), height: 7390, time: now - 26000, from: addr(), to: me, amount: 3.2 * UNIT, fee: 0.001 * UNIT },
      { txid: hex(64), height: 7301, time: now - 90000, from: me, to: addr(), amount: 0.75 * UNIT, fee: 0.001 * UNIT }
    ];
  }

  var App = {
    WalletState: function () { return ms({ Exists: W.exists, Encrypted: W.encrypted, Locked: W.locked, AddressCount: W.keys.length }); },
    CreateWallet: function (pass) {
      W.exists = true; W.encrypted = !!pass; W.locked = false; W.pass = pass || "";
      var k = newKey("Main", 113.337 * UNIT); W.keys.push(k); seedHistory();
      return ms({ Label: k.Label, Addr: k.Addr });
    },
    Unlock: function (pass) { if (W.encrypted && pass !== W.pass) return ms(false); W.locked = false; return ms(true); },
    Lock: function () { W.locked = true; return ms(null); },
    IsLocked: function () { return ms(W.locked); },
    CreateAddress: function (label) { var k = newKey(label || ("Address " + (W.keys.length + 1)), 0); W.keys.push(k); return ms({ Label: k.Label, Addr: k.Addr }); },
    ListAddresses: function () { return ms(W.keys.map(function (k) { return { Label: k.Label, Addr: k.Addr, Balance: crb(k._bal) }; })); },
    TotalBalance: function () { return ms(crb(W.keys.reduce(function (s, k) { return s + k._bal; }, 0))); },
    ImportKey: function (priv, label) {
      if (!/^[0-9a-fA-F]{128}$/.test(priv || "")) return fail("invalid private key (need 128 hex chars)");
      var k = newKey(label || "Imported", 0); k._priv = priv; W.keys.push(k); return ms({ Label: k.Label, Addr: k.Addr });
    },
    ExportKey: function (q, pass) { if (W.encrypted && pass !== W.pass) return fail("wrong passphrase"); var k = find(q); return k ? ms(k._priv) : fail("address not found"); },
    EncryptWallet: function (pass) { W.encrypted = true; W.pass = pass; return ms(null); },
    ChangePassphrase: function (oldp, newp) { if (W.encrypted && oldp !== W.pass) return fail("wrong current passphrase"); W.pass = newp; W.encrypted = true; return ms(null); },
    SuggestedFee: function () { return ms("0.00100000"); },
    ValidateAddress: function (a) { return ms(/^crb1[0-9a-fA-F]{40}$/.test(a || "")); },
    Send: function (from, to, amt, fee) {
      if (!/^crb1[0-9a-fA-F]{40}$/.test(to || "")) return fail("bad destination address");
      var a = parseFloat(amt); if (!(a > 0)) return fail("amount must be positive");
      var k = from ? find(from) : W.keys[0];
      if (!k || k._bal < a * UNIT) return fail("insufficient spendable balance");
      k._bal -= (a + parseFloat(fee || "0.001")) * UNIT;
      var tx = { txid: hex(64), height: 0, time: Math.floor(Date.now() / 1000), from: k.Addr, to: to, amount: a * UNIT, fee: parseFloat(fee || "0.001") * UNIT };
      fakeHist.unshift(tx);
      return ms({ Txid: tx.txid, To: to, Amount: crb(a * UNIT) });
    },
    SpeedUp: function (txid) { return ms({ Txid: hex(64), To: addr(), Amount: crb(12.5 * UNIT) }); },
    Cancel: function (txid) { var me = W.keys[0] ? W.keys[0].Addr : addr(); return ms({ Txid: hex(64), To: me, Amount: crb(1) }); },
    History: function (q) {
      var rows = fakeHist.slice();
      if (q) { var k = find(q); if (k) rows = rows.filter(function (r) { return r.from === k.Addr || r.to === k.Addr; }); }
      return ms(rows);
    },
    NetworkStatus: function () {
      return ms({
        height: W.height, tip: hex(64), time: Math.floor(Date.now() / 1000) - 22, supply: 371050 * UNIT,
        difficulty: "1284773", hashrate: 1843000, reward: 50 * UNIT, mempool: 3, peers: 8, epoch: 1,
        block_age: 22, fee_suggested: 0.001 * UNIT, node_version: "2.4.1", consensus_version: 4
      });
    },
    GetBlock: function (q) {
      var h = /^\d+$/.test(q) ? parseInt(q, 10) : 7420;
      return ms({ v: 1, height: h, time: Math.floor(Date.now() / 1000) - 60, prev: hex(64), txroot: hex(64), target: hex(64), nonce: 8472913,
        txs: [{ from_pub: "", to: W.keys[0] ? W.keys[0].Addr : addr(), amount: 50 * UNIT, fee: 0, nonce: h, sig: hex(40) },
              { from_pub: hex(64), to: addr(), amount: 4.5 * UNIT, fee: 0.001 * UNIT, nonce: 12, sig: hex(128) }] });
    },
    GetTx: function (id) { return ms({ found: true, pending: false, txid: id, height: 7411, block_hash: hex(64), time: Math.floor(Date.now() / 1000) - 5400, from: W.keys[0] ? W.keys[0].Addr : addr(), to: addr(), amount: 12.5 * UNIT, fee: 0.001 * UNIT, nonce: 4, coinbase: false }); },
    AddressInfo: function (a) { var k = find(a); var bal = k ? k._bal : 88 * UNIT; return ms({ address: a, balance: bal, nonce: 5, spendable: bal, received: 200 * UNIT, mined: 150 * UNIT, sent: 62 * UNIT, txn: 14 }); },
    Richlist: function (n) { var out = []; var base = 50000 * UNIT; for (var i = 0; i < (n || 25); i++) out.push({ address: i === 2 && W.keys[0] ? W.keys[0].Addr : addr(), balance: Math.floor(base / (i + 1)), nonce: i }); return ms(out); },
    Mempool: function () { return ms([{ from_pub: hex(64), to: addr(), amount: 1.1 * UNIT, fee: 0.001 * UNIT, nonce: 7, sig: hex(128) }]); },
    Search: function (q) {
      q = (q || "").trim();
      if (/^crb1[0-9a-fA-F]{40}$/.test(q)) return ms({ type: "address", value: q });
      if (/^\d+$/.test(q)) return ms({ type: "block", height: parseInt(q, 10) });
      if (/^[0-9a-fA-F]{64}$/.test(q)) return ms({ type: "tx", value: q });
      return fail("unrecognized query");
    },
    GetSettings: function () { return ms(JSON.parse(JSON.stringify(W.settings))); },
    SetNodeMode: function (mode, url) { W.settings.NodeMode = mode; if (mode === "custom" && url) W.settings.Endpoint = url; else if (mode === "lite") W.settings.Endpoint = W.settings.Endpoints[0]; else if (mode === "full") W.settings.Endpoint = "http://127.0.0.1:18751"; return ms(null); },
    NodeInfo: function () { return ms({ Mode: W.settings.NodeMode, Endpoint: W.settings.Endpoint, Reachable: true, Syncing: false, Height: W.height, SyncHeight: W.height }); },
    StartFullNode: function () { W.settings.NodeMode = "full"; return ms(null); },
    StopFullNode: function () { W.settings.NodeMode = "lite"; return ms(null); },
    SetLockTimeout: function (min) { W.settings.LockTimeoutMin = min | 0; return ms(null); },
    // No signed manifest exists in a plain-browser preview, so report no update.
    CheckUpdate: function () { return ms({ Available: false, Version: "", Notes: "", URL: "", Sha256: "" }); },
    OpenExternal: function (url) { try { global.open(url, "_blank"); } catch (e) {} return ms(null); }
  };

  global.go = { main: { App: App } };
  console.info("[Cereblix] dev mock backend active (no Wails bindings detected).");
})(window);
