/* ============================================================================
   store.js — shared client state + tiny pub/sub. No framework.
   Holds the cached wallet state, address list, network status and settings so
   views don't each re-fetch on every navigation. Exposed as window.Store.
   ========================================================================== */
(function (global) {
  "use strict";
  var API = global.API, UI = global.UI;

  var listeners = {};
  function on(evt, fn) { (listeners[evt] = listeners[evt] || []).push(fn); return function () { off(evt, fn); }; }
  function off(evt, fn) { if (listeners[evt]) listeners[evt] = listeners[evt].filter(function (f) { return f !== fn; }); }
  function emit(evt, data) { (listeners[evt] || []).forEach(function (f) { try { f(data); } catch (e) { console.error(e); } }); }

  var state = {
    wallet: null,        // {Exists,Encrypted,Locked,AddressCount}
    addresses: [],       // [{Label,Addr,Balance}]
    total: "0",
    status: null,        // network status (lowercase json fields)
    node: null,          // NodeInfo {Mode,Endpoint,Reachable,Syncing,Height,SyncHeight}
    settings: null,
    addrSet: {},         // map addr->true for "you" tagging
    lastActivity: Date.now()
  };

  function refreshWallet() {
    return API.WalletState().then(function (w) { state.wallet = w; emit("wallet", w); return w; });
  }
  function refreshAddresses() {
    return Promise.all([API.ListAddresses(), API.TotalBalance()]).then(function (r) {
      state.addresses = r[0] || [];
      state.total = r[1] || "0";
      state.addrSet = {};
      state.addresses.forEach(function (a) { state.addrSet[a.Addr] = true; });
      emit("addresses", state.addresses);
      return state.addresses;
    });
  }
  function refreshStatus() {
    return API.NetworkStatus().then(function (s) { state.status = s; emit("status", s); return s; })
      .catch(function (e) { state.status = null; emit("status", null); throw e; });
  }
  function refreshNode() {
    return API.NodeInfo().then(function (n) { state.node = n; emit("node", n); return n; })
      .catch(function (e) { state.node = null; emit("node", null); });
  }
  function refreshSettings() {
    return API.GetSettings().then(function (s) { state.settings = s; emit("settings", s); return s; });
  }

  function isOurs(addr) { return !!state.addrSet[addr]; }
  function height() { return state.status ? Number(state.status.height) : (state.node ? Number(state.node.Height) : 0); }
  function markActivity() { state.lastActivity = Date.now(); }

  // background pollers (started by app.js once unlocked)
  var statusTimer = null, nodeTimer = null;
  function startPolling() {
    stopPolling();
    refreshStatus().catch(function () {}); refreshNode();
    statusTimer = setInterval(function () { refreshStatus().catch(function () {}); }, 12000);
    nodeTimer = setInterval(refreshNode, 15000);
  }
  function stopPolling() {
    if (statusTimer) clearInterval(statusTimer); statusTimer = null;
    if (nodeTimer) clearInterval(nodeTimer); nodeTimer = null;
  }

  global.Store = {
    state: state, on: on, off: off, emit: emit,
    refreshWallet: refreshWallet, refreshAddresses: refreshAddresses, refreshStatus: refreshStatus,
    refreshNode: refreshNode, refreshSettings: refreshSettings,
    isOurs: isOurs, height: height, markActivity: markActivity,
    startPolling: startPolling, stopPolling: stopPolling
  };
})(window);
