/* ============================================================================
   api.js — thin, typed-ish wrappers over the Wails Go bindings.
   Every method maps 1:1 to window.go.main.App.<Method>. Errors are normalized
   to Error(message) so views can `catch (e) { show(e.message) }`.
   Exposed as window.API.
   ========================================================================== */
(function (global) {
  "use strict";

  function backend() {
    return (global.go && global.go.main && global.go.main.App) || null;
  }

  // Resolve once the Wails bindings have been injected (they usually exist at
  // load, but guard against a late inject so the app never calls undefined).
  function ready(timeoutMs) {
    return new Promise(function (resolve) {
      if (backend()) return resolve(true);
      var waited = 0, step = 40, max = timeoutMs || 6000;
      var t = setInterval(function () {
        if (backend()) { clearInterval(t); resolve(true); }
        else if ((waited += step) >= max) { clearInterval(t); resolve(false); }
      }, step);
    });
  }

  function call(method, args) {
    var b = backend();
    if (!b || typeof b[method] !== "function") {
      return Promise.reject(new Error("Backend method " + method + "() is unavailable"));
    }
    var p;
    try { p = b[method].apply(b, args || []); }
    catch (e) { return Promise.reject(normalize(e)); }
    return Promise.resolve(p).catch(function (e) { throw normalize(e); });
  }
  function normalize(e) {
    if (e instanceof Error) return e;
    if (typeof e === "string") return new Error(e);
    if (e && e.message) return new Error(e.message);
    try { return new Error(JSON.stringify(e)); } catch (_) { return new Error("Unknown error"); }
  }

  var API = {
    ready: ready,
    available: function () { return !!backend(); },

    // ---- wallet lifecycle ----
    WalletState:       function () { return call("WalletState"); },
    CreateWallet:      function (pass) { return call("CreateWallet", [pass || ""]); },
    Unlock:            function (pass) { return call("Unlock", [pass || ""]); },
    Lock:              function () { return call("Lock"); },
    IsLocked:          function () { return call("IsLocked"); },

    // ---- addresses / keys ----
    CreateAddress:     function (label) { return call("CreateAddress", [label || ""]); },
    ListAddresses:     function () { return call("ListAddresses"); },
    TotalBalance:      function () { return call("TotalBalance"); },
    ImportKey:         function (privHex, label) { return call("ImportKey", [privHex, label || ""]); },
    ExportKey:         function (addrOrLabel, pass) { return call("ExportKey", [addrOrLabel, pass || ""]); },
    EncryptWallet:     function (pass) { return call("EncryptWallet", [pass]); },
    ChangePassphrase:  function (oldp, newp) { return call("ChangePassphrase", [oldp, newp]); },

    // ---- sending ----
    Send:              function (from, to, amountCRB, feeCRB) { return call("Send", [from || "", to, amountCRB, feeCRB || ""]); },
    SpeedUp:           function (txid, feeCRB) { return call("SpeedUp", [txid, feeCRB || ""]); },
    Cancel:            function (txid, feeCRB) { return call("Cancel", [txid, feeCRB || ""]); },
    SuggestedFee:      function () { return call("SuggestedFee"); },
    ValidateAddress:   function (addr) { return call("ValidateAddress", [addr]); },

    // ---- history ----
    History:           function (addrOrLabel) { return call("History", [addrOrLabel || ""]); },

    // ---- explorer / network ----
    NetworkStatus:     function () { return call("NetworkStatus"); },
    GetBlock:          function (q) { return call("GetBlock", [q]); },
    GetTx:             function (txid) { return call("GetTx", [txid]); },
    AddressInfo:       function (addr) { return call("AddressInfo", [addr]); },
    Richlist:          function (n) { return call("Richlist", [n || 25]); },
    Mempool:           function () { return call("Mempool"); },
    Search:            function (q) { return call("Search", [q]); },

    // ---- settings / node ----
    GetSettings:       function () { return call("GetSettings"); },
    SetNodeMode:       function (mode, customURL) { return call("SetNodeMode", [mode, customURL || ""]); },
    NodeInfo:          function () { return call("NodeInfo"); },
    StartFullNode:     function () { return call("StartFullNode"); },
    StopFullNode:      function () { return call("StopFullNode"); }
  };

  global.API = API;
})(window);
