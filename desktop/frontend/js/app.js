/* ============================================================================
   app.js — bootstrap, shell, routing-guards, auto-lock. Exposed as window.App.
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, API = global.API, Store = global.Store, Router = global.Router, Views = global.Views, el = UI.el;

  var root = document.getElementById("app");
  var mode = null;            // 'auth' | 'app'
  var shell = null;           // cached shell refs
  var leaveFns = [];          // teardown callbacks for the active view
  var lockTimer = null, lastReset = 0, footerWired = false;
  var updateInfo = null, updateChecked = false, updateDismissed = false; // signed-update banner

  var NAV = [
    { name: "dashboard", icon: "dashboard", label: "Dashboard" },
    { name: "send", icon: "send", label: "Send" },
    { name: "receive", icon: "receive", label: "Receive" },
    { name: "history", icon: "history", label: "History" },
    { name: "addresses", icon: "wallet", label: "Addresses" },
    { name: "explorer", icon: "explorer", label: "Explorer" },
    { name: "settings", icon: "settings", label: "Settings" }
  ];

  // ------------------------------ boot ------------------------------
  function boot() {
    UI.clear(root); root.appendChild(UI.loading("Starting Cereblix Wallet…"));
    API.ready().then(function (ok) {
      if (!ok) { UI.clear(root); root.appendChild(splashError()); return; }
      return Store.refreshSettings().catch(function () {})
        .then(Store.refreshWallet)
        .then(function () { Router.start(onRoute); installActivity(); })
        .catch(function (e) { UI.clear(root); root.appendChild(splashError(e.message)); });
    });
  }
  function splashError(msg) {
    return el("div", { class: "fullscreen" }, [el("div", { class: "auth-card" }, [
      el("div", { class: "auth-brand" }, [el("div", { class: "logo" }), el("h1", { text: "Cereblix Wallet" })]),
      UI.banner(msg || "Could not reach the wallet backend. Please restart the app.", "err"),
      el("button", { class: "btn btn-primary btn-block", text: "Retry", onclick: boot })
    ])]);
  }

  // --------------------------- route guard ---------------------------
  function onRoute(route) {
    var w = Store.state.wallet || { Exists: false, Locked: false };
    if (!w.Exists) return showAuth(Views.onboarding);
    if (w.Locked) return showAuth(Views.lock);
    showApp(route.name, route.params);
  }

  function showAuth(view) {
    runLeave();
    Store.stopPolling(); clearLockTimer();
    mode = "auth"; shell = null;
    UI.clear(root);
    view.render(root);
  }

  // --------------------------- app shell ---------------------------
  function buildShell() {
    var navEl = el("nav", { class: "nav", role: "navigation", "aria-label": "Primary" });
    NAV.forEach(function (item) {
      var b = el("button", { class: "nav-item", "data-route": item.name, html: UI.icon(item.icon, 18) + '<span class="label-txt">' + UI.escapeHtml(item.label) + "</span>" });
      b.addEventListener("click", function () { Router.go(item.name); });
      navEl.appendChild(b);
    });

    var nodePill = el("button", { class: "node-pill", id: "node-pill", title: "Network status", onclick: function () { Router.go("settings"); } }, [
      el("span", { class: "dot" }), el("span", { class: "meta" }, [el("b", { text: "Node" }), el("span", { class: "endpoint", text: "—" })])
    ]);
    var lockBtn = el("button", { class: "btn btn-sm btn-ghost", id: "lock-btn", html: UI.icon("lock", 15) + "<span>Lock</span>", onclick: lockNow });

    var titleEl = el("h1", { id: "tb-title" });
    var subEl = el("div", { class: "sub", id: "tb-sub" });
    var actionsEl = el("div", { class: "actions", id: "tb-actions" });
    var viewEl = el("div", { class: "inner", id: "view" });
    var updateHost = el("div", { class: "update-host", id: "update-host" });

    var layout = el("div", { class: "layout" }, [
      el("aside", { class: "sidebar" }, [
        el("div", { class: "brand" }, [
          el("img", { class: "brand-logo", src: "assets/logo.svg", alt: "Cereblix", width: "34", height: "34" }),
          el("div", { class: "name brand-wordmark" }, [
            el("span", { class: "brand-word" }, ["Cereb", el("span", { class: "brand-accent", text: "lix" })]),
            el("small", { text: "Wallet" })
          ])
        ]),
        navEl,
        el("div", { class: "sidebar-foot" }, [nodePill, lockBtn])
      ]),
      el("div", { class: "main" }, [
        updateHost,
        el("header", { class: "topbar" }, [el("div", {}, [titleEl, subEl]), el("div", { class: "spacer" }), actionsEl]),
        el("div", { class: "content" }, [viewEl])
      ])
    ]);

    UI.clear(root); root.appendChild(layout);
    shell = { navEl: navEl, titleEl: titleEl, subEl: subEl, actionsEl: actionsEl, viewEl: viewEl, nodePill: nodePill, lockBtn: lockBtn, updateHost: updateHost };
    renderUpdateBanner();
    updateFooter();
    // Wire footer updates once (updateFooter reads the module-level `shell`), so
    // repeated lock/unlock shell rebuilds never accumulate stale subscriptions.
    if (!footerWired) {
      footerWired = true;
      Store.on("node", updateFooter); Store.on("status", updateFooter); Store.on("wallet", updateFooter);
    }
  }

  function showApp(name, params) {
    if (Router.APP_ROUTES.indexOf(name) === -1) name = "dashboard";
    if (mode !== "app" || !shell) { mode = "app"; buildShell(); Store.startPolling(); resetLockTimer(); maybeCheckUpdate(); }
    mountView(name, params || {});
  }

  // --------------------------- signed update banner ---------------------------
  // Runs once per session, after the wallet is unlocked. CheckUpdate only returns
  // Available:true for a manifest whose ed25519 signature verifies against the
  // pinned release key AND that advertises a strictly-newer version (all enforced
  // in Go). A dismiss only hides it for this session; it shows again next launch.
  function maybeCheckUpdate() {
    if (updateChecked) return;
    updateChecked = true;
    API.CheckUpdate().then(function (info) {
      if (info && info.Available) { updateInfo = info; renderUpdateBanner(); }
    }).catch(function () {});
  }
  function renderUpdateBanner() {
    if (!shell || !shell.updateHost) return;
    UI.clear(shell.updateHost);
    if (!updateInfo || updateDismissed) return;
    var bar = el("div", { class: "update-banner", role: "status" }, [
      el("span", { class: "ub-dot" }),
      el("span", { class: "ub-txt" }, [
        el("b", { text: "Update available — v" + (updateInfo.Version || "") }),
        updateInfo.Notes ? el("span", { class: "ub-notes", text: " · " + updateInfo.Notes }) : null
      ]),
      el("button", { class: "btn btn-sm btn-primary", text: "Update", onclick: function () {
        if (!updateInfo.URL) { UI.toast("No download link provided", "err"); return; }
        API.OpenExternal(updateInfo.URL).catch(function (e) { UI.toast(e.message, "err"); });
      } }),
      el("button", { class: "btn btn-sm btn-ghost btn-icon", title: "Dismiss", "aria-label": "Dismiss update notice", html: UI.icon("x", 16),
        onclick: function () { updateDismissed = true; renderUpdateBanner(); } })
    ]);
    shell.updateHost.appendChild(bar);
  }

  function mountView(name, params) {
    runLeave();
    var view = Views[name] || Views.dashboard;
    // active nav
    UI.$$(".nav-item", shell.navEl).forEach(function (b) { b.classList.toggle("active", b.getAttribute("data-route") === name); });
    // topbar
    shell.titleEl.textContent = view.title || "";
    shell.subEl.textContent = ""; shell.subEl.style.display = "none";
    UI.clear(shell.actionsEl);
    // view body
    UI.clear(shell.viewEl);
    var body = el("div", { class: "fade-in" });
    shell.viewEl.appendChild(body);
    shell.viewEl.parentElement.scrollTop = 0;

    var ctx = {
      params: params,
      setActions: function (nodes) { UI.clear(shell.actionsEl); UI.append(shell.actionsEl, nodes); },
      setSub: function (text) { shell.subEl.textContent = text || ""; shell.subEl.style.display = text ? "block" : "none"; },
      onLeave: function (fn) { if (typeof fn === "function") leaveFns.push(fn); }
    };
    try { view.render(body, params, ctx); }
    catch (e) { console.error(e); UI.clear(body); body.appendChild(UI.banner("Something went wrong rendering this screen: " + e.message, "err")); }
  }

  function runLeave() { leaveFns.forEach(function (fn) { try { fn(); } catch (e) {} }); leaveFns = []; }

  function updateFooter() {
    if (!shell) return;
    var n = Store.state.node, s = Store.state.status, w = Store.state.wallet;
    var pill = shell.nodePill;
    pill.className = "node-pill " + (n ? (n.Reachable ? (n.Syncing ? "sync" : "ok") : "bad") : "");
    var b = pill.querySelector("b"), ep = pill.querySelector(".endpoint");
    if (n) {
      b.textContent = n.Reachable ? (n.Syncing ? "Syncing" : "Connected") : "Offline";
      ep.textContent = (n.Mode || "lite") + (s ? " · h" + UI.num(s.height) : (n.Height ? " · h" + UI.num(n.Height) : ""));
    } else { b.textContent = "Node"; ep.textContent = "checking…"; }
    shell.lockBtn.style.display = (w && w.Encrypted) ? "" : "none";
  }

  // --------------------------- transitions ---------------------------
  function enterApp() {
    Store.markActivity();
    Promise.all([Store.refreshWallet(), Store.refreshAddresses().catch(function () {})]).then(function () {
      mode = null;              // force shell rebuild
      Router.go("dashboard");   // triggers exactly one dispatch (hashchange or direct)
    });
  }

  function lockNow() {
    API.Lock().then(function () { return Store.refreshWallet(); }).then(function () {
      clearLockTimer(); Store.stopPolling();
      Router.dispatch(); // guard will show lock screen
    }).catch(function (e) { UI.toast(e.message, "err"); });
  }

  // --------------------------- auto-lock ---------------------------
  function lockTimeoutMin() {
    var ov = global.localStorage && localStorage.getItem("crb.lockTimeoutMin");
    if (ov != null && ov !== "") return Number(ov);
    var s = Store.state.settings;
    return s && s.LockTimeoutMin != null ? Number(s.LockTimeoutMin) : 10;
  }
  function setLockTimeout(min) {
    try { localStorage.setItem("crb.lockTimeoutMin", String(min)); } catch (e) {}
    // Persist to the backend too, so the backend idle-lock fail-safe (which wipes
    // keys independently of this renderer) honors the same timeout the user picked.
    if (API.SetLockTimeout) API.SetLockTimeout(min).catch(function () {});
    resetLockTimer();
  }
  function clearLockTimer() { if (lockTimer) clearTimeout(lockTimer); lockTimer = null; }
  function resetLockTimer() {
    clearLockTimer();
    var w = Store.state.wallet;
    var min = lockTimeoutMin();
    if (mode === "app" && w && w.Encrypted && min > 0) {
      lockTimer = setTimeout(function () { UI.toast("Locked due to inactivity", "info"); lockNow(); }, min * 60000);
    }
  }
  function installActivity() {
    var handler = function () {
      var now = Date.now();
      if (now - lastReset < 1500) return; // throttle
      lastReset = now; Store.markActivity(); resetLockTimer();
    };
    ["mousemove", "mousedown", "keydown", "scroll", "touchstart"].forEach(function (ev) {
      document.addEventListener(ev, handler, { passive: true });
    });
    // When the window regains focus, re-check the backend lock state directly:
    // if it locked out-of-band (e.g. a future system event), reflect it in the UI.
    document.addEventListener("visibilitychange", function () {
      if (document.visibilityState !== "visible" || mode !== "app") return;
      API.IsLocked().then(function (locked) {
        if (locked) { Store.state.wallet = Object.assign({}, Store.state.wallet, { Locked: true }); clearLockTimer(); Store.stopPolling(); Router.dispatch(); }
      }).catch(function () {});
    });
  }

  global.App = {
    boot: boot, enterApp: enterApp, lockNow: lockNow,
    lockTimeoutMin: lockTimeoutMin, setLockTimeout: setLockTimeout
  };

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})(window);
