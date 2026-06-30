/* ============================================================================
   views/settings.js — node mode, security/passphrase, auto-lock, about.
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, API = global.API, Store = global.Store, el = UI.el;
  global.Views = global.Views || {};

  function render(mount, params, ctx) {
    var settings = Store.state.settings || { NodeMode: "lite", Endpoint: "", Endpoints: [], LockTimeoutMin: 10 };

    mount.appendChild(nodeCard());
    mount.appendChild(securityCard());
    mount.appendChild(lockCard());
    mount.appendChild(aboutCard());

    Store.refreshNode();
    ctx.onLeave(Store.on("node", function () { fillNodeStatus(); }));

    // --------------------------- node mode ---------------------------
    function nodeCard() {
      var mode = settings.NodeMode || "lite";
      var seg = el("div", { class: "segmented", role: "tablist" });
      ["lite", "full", "custom"].forEach(function (m) {
        var b = el("button", { class: (m === mode ? "active" : ""), role: "tab", text: m === "lite" ? "Lite" : (m === "full" ? "Full node" : "Custom") });
        b.addEventListener("click", function () { switchMode(m); });
        seg.appendChild(b);
      });

      var body = el("div", { id: "node-body", style: "margin-top:18px" });
      var card = el("div", { class: "card card-pad" }, [
        el("div", { class: "card-head" }, [el("span", { html: UI.icon("node", 18), style: "color:var(--accent)" }), el("h2", { text: "Network" }), el("div", { class: "spacer" }), seg]),
        body,
        el("div", { id: "node-status", style: "margin-top:18px" })
      ]);
      renderModeBody(mode, body);
      fillNodeStatus();
      return card;
    }

    function renderModeBody(mode, body) {
      UI.clear(body);
      if (mode === "lite") {
        body.appendChild(el("p", { class: "dim", style: "margin:0 0 12px", text: "Connects to a hosted Cereblix node over HTTPS. Lightweight — no local sync. Recommended." }));
        body.appendChild(el("div", { class: "field", style: "margin:0" }, [
          el("label", { text: "Active endpoint" }),
          el("div", { class: "review" }, [el("div", { class: "r-line" }, [UI.copyable(settings.Endpoint || "—", { display: settings.Endpoint || "—" })])]),
          (settings.Endpoints && settings.Endpoints.length > 1) ? el("div", { class: "hint", text: "Fallbacks: " + settings.Endpoints.slice(1).join(", ") }) : null
        ]));
      } else if (mode === "full") {
        body.appendChild(el("p", { class: "dim", style: "margin:0 0 12px", text: "Runs an embedded Cereblix node in-process and validates the chain yourself. Uses more disk, CPU and bandwidth, and needs time to sync." }));
        body.appendChild(el("div", { class: "btn-row" }, [
          el("button", { class: "btn btn-sm", html: UI.icon("plug", 15) + "<span>Start full node</span>", onclick: function () { act(API.StartFullNode(), "Full node starting…"); } }),
          el("button", { class: "btn btn-sm btn-ghost", html: UI.icon("x", 15) + "<span>Stop</span>", onclick: function () { act(API.StopFullNode(), "Full node stopping…"); } })
        ]));
      } else {
        var url = el("input", { class: "input mono", placeholder: "https://my-node.example/api", value: (settings.NodeMode === "custom" ? settings.Endpoint : "") });
        var err = el("div", { style: "margin-top:8px" });
        body.appendChild(el("div", { class: "field", style: "margin:0" }, [
          el("label", { text: "Custom node RPC URL" }), url,
          el("div", { class: "hint", text: "Point to any Cereblix node's HTTP RPC base — include the /api path (e.g. http://127.0.0.1:18751/api)." }),
          err,
          el("div", { style: "margin-top:12px" }, [el("button", { class: "btn btn-sm btn-primary", text: "Save endpoint", onclick: function () {
            var v = url.value.trim();
            if (!/^https?:\/\//i.test(v)) { UI.clear(err); err.appendChild(UI.banner("Enter a full http(s):// URL.", "err")); return; }
            act(API.SetNodeMode("custom", v), "Endpoint saved");
          } })])
        ]));
      }
    }

    function switchMode(m) {
      var body = UI.$("#node-body");
      if (m === "custom") { renderModeBody("custom", body); markSeg(m); return; }
      if (m === "full") {
        UI.confirm({ title: "Switch to full node?", message: "The wallet will run an embedded node and sync the chain locally. This can take a while and use significant resources.", okText: "Enable full node" })
          .then(function (yes) { if (yes) act(API.SetNodeMode("full", ""), "Switched to full node"); else markSeg(settings.NodeMode); });
        renderModeBody("full", body); markSeg("full"); return;
      }
      act(API.SetNodeMode("lite", ""), "Switched to lite mode");
    }
    function markSeg(m) {
      var seg = UI.$(".segmented"); if (!seg) return;
      UI.$$("button", seg).forEach(function (b) { b.classList.toggle("active", b.textContent.toLowerCase().indexOf(m === "custom" ? "custom" : (m === "full" ? "full" : "lite")) !== -1); });
    }

    function act(promise, okMsg) {
      promise.then(function () {
        return Store.refreshSettings().then(function (s) { settings = s; return Store.refreshNode(); });
      }).then(function () {
        UI.toast(okMsg, "ok");
        var body = UI.$("#node-body"); if (body) renderModeBody(settings.NodeMode, body);
        markSeg(settings.NodeMode); fillNodeStatus();
      }).catch(function (e) { UI.toast(e.message, "err", 4500); });
    }

    function fillNodeStatus() {
      var host = UI.$("#node-status"); if (!host) return;
      UI.clear(host);
      var n = Store.state.node;
      if (!n) { host.appendChild(el("div", { class: "dim", style: "font-size:13px" }, [el("span", { class: "spinner", style: "display:inline-block;vertical-align:middle;margin-right:8px" }), "Checking node…"])); return; }
      var dotClass = n.Reachable ? (n.Syncing ? "sync" : "ok") : "bad";
      var pct = (n.SyncHeight && n.Height) ? Math.min(100, Math.floor((Number(n.Height) / Number(n.SyncHeight)) * 100)) : null;
      host.appendChild(el("div", { class: "node-pill " + dotClass, style: "cursor:default;display:inline-flex" }, [
        el("span", { class: "dot" }),
        el("span", { class: "meta" }, [
          el("b", { text: (n.Reachable ? (n.Syncing ? "Syncing" : "Connected") : "Unreachable") + " · " + (n.Mode || settings.NodeMode) }),
          el("span", { text: "Height " + UI.num(n.Height) + (n.Syncing && pct != null ? " · " + pct + "%" : "") })
        ])
      ]));
    }

    // --------------------------- security ---------------------------
    function securityCard() {
      var enc = Store.state.wallet && Store.state.wallet.Encrypted;
      var body = el("div");
      var card = el("div", { class: "card card-pad" }, [
        el("div", { class: "card-head" }, [el("span", { html: UI.icon("lock", 18), style: "color:var(--accent)" }), el("h2", { text: "Security" })]),
        body
      ]);
      body.appendChild(el("div", { class: "about-line" }, [el("span", { text: "Wallet encryption" }),
        el("span", {}, [enc ? el("span", { class: "tag in", text: "Encrypted (AES-GCM)" }) : el("span", { class: "tag pend", text: "Not encrypted" })])]));
      body.appendChild(el("div", { style: "margin-top:16px" }, [
        enc
          ? el("button", { class: "btn btn-sm", html: UI.icon("key", 15) + "<span>Change passphrase</span>", onclick: changePass })
          : el("button", { class: "btn btn-sm btn-primary", html: UI.icon("lock", 15) + "<span>Encrypt wallet</span>", onclick: encryptWallet })
      ]));
      return card;
    }

    function encryptWallet() {
      var p1 = el("input", { class: "input", type: "password", placeholder: "New passphrase", autocomplete: "new-password", autofocus: true });
      var p2 = el("input", { class: "input", type: "password", placeholder: "Confirm passphrase", autocomplete: "new-password" });
      var err = el("div");
      var m = UI.modal({
        title: "Encrypt wallet",
        body: [UI.banner("Encrypts wallet.json at rest with PBKDF2-SHA256 → AES-GCM. You'll need this passphrase to unlock.", "info"), err,
          el("div", { class: "field" }, [el("label", { text: "Passphrase" }), p1]),
          el("div", { class: "field", style: "margin:0" }, [el("label", { text: "Confirm" }), p2])],
        footer: [el("button", { class: "btn btn-ghost", text: "Cancel", onclick: function () { m.close(); } }),
          el("button", { class: "btn btn-primary", text: "Encrypt", onclick: go })]
      });
      function go() {
        UI.clear(err);
        if (p1.value.length < 8) { err.appendChild(UI.banner("Use at least 8 characters.", "err")); return; }
        if (p1.value !== p2.value) { err.appendChild(UI.banner("Passphrases do not match.", "err")); return; }
        API.EncryptWallet(p1.value).then(function () { return Store.refreshWallet(); })
          .then(function () { m.close(); UI.toast("Wallet encrypted", "ok"); reRender(); })
          .catch(function (e) { err.appendChild(UI.banner(e.message, "err")); });
      }
    }

    function changePass() {
      var o = el("input", { class: "input", type: "password", placeholder: "Current passphrase", autocomplete: "current-password", autofocus: true });
      var p1 = el("input", { class: "input", type: "password", placeholder: "New passphrase", autocomplete: "new-password" });
      var p2 = el("input", { class: "input", type: "password", placeholder: "Confirm new passphrase", autocomplete: "new-password" });
      var err = el("div");
      var m = UI.modal({
        title: "Change passphrase",
        body: [err,
          el("div", { class: "field" }, [el("label", { text: "Current" }), o]),
          el("div", { class: "field" }, [el("label", { text: "New" }), p1]),
          el("div", { class: "field", style: "margin:0" }, [el("label", { text: "Confirm new" }), p2])],
        footer: [el("button", { class: "btn btn-ghost", text: "Cancel", onclick: function () { m.close(); } }),
          el("button", { class: "btn btn-primary", text: "Update", onclick: go })]
      });
      function go() {
        UI.clear(err);
        if (p1.value.length < 8) { err.appendChild(UI.banner("New passphrase must be at least 8 characters.", "err")); return; }
        if (p1.value !== p2.value) { err.appendChild(UI.banner("New passphrases do not match.", "err")); return; }
        API.ChangePassphrase(o.value, p1.value).then(function () { m.close(); UI.toast("Passphrase updated", "ok"); })
          .catch(function (e) { err.appendChild(UI.banner(e.message, "err")); o.value = ""; o.focus(); });
      }
    }

    function reRender() { UI.clear(mount); render(mount, params, ctx); }

    // --------------------------- auto-lock ---------------------------
    function lockCard() {
      var current = global.App.lockTimeoutMin();
      var sel = el("select", { class: "select", style: "max-width:220px" }, [
        [0, "Never"], [1, "1 minute"], [5, "5 minutes"], [10, "10 minutes"], [15, "15 minutes"], [30, "30 minutes"], [60, "1 hour"]
      ].map(function (o) { return el("option", { value: o[0], selected: Number(current) === o[0], text: o[1] }); }));
      sel.addEventListener("change", function () { global.App.setLockTimeout(Number(sel.value)); UI.toast("Auto-lock updated", "ok"); });

      return el("div", { class: "card card-pad" }, [
        el("div", { class: "card-head" }, [el("span", { html: UI.icon("history", 18), style: "color:var(--accent)" }), el("h2", { text: "Auto-lock" })]),
        el("div", { class: "about-line", style: "border:0;padding-top:0" }, [
          el("span", {}, [el("div", { text: "Lock after inactivity" }), el("div", { class: "faint", style: "font-size:12px", text: "Applied on this device." })]),
          sel
        ]),
        el("div", { style: "margin-top:8px" }, [el("button", { class: "btn btn-sm btn-ghost", html: UI.icon("lock", 15) + "<span>Lock now</span>", onclick: function () { global.App.lockNow(); } })])
      ]);
    }

    // --------------------------- about ---------------------------
    function aboutCard() {
      var s = Store.state.status;
      return el("div", { class: "card card-pad" }, [
        el("div", { class: "card-head" }, [el("span", { html: UI.icon("info", 18), style: "color:var(--accent)" }), el("h2", { text: "About" })]),
        el("div", {}, [
          aboutLine("Application", "Cereblix Wallet"),
          aboutLine("Ticker", "CRB"),
          aboutLine("Algorithm", "NeuroMorph (CPU PoW)"),
          aboutLine("Address format", "crb1…"),
          s && s.node_version ? aboutLine("Connected node", "v" + s.node_version) : null,
          s && s.consensus_version != null ? aboutLine("Consensus version", "v" + s.consensus_version) : null
        ]),
        el("p", { class: "faint", style: "margin:16px 0 0;font-size:12px", text: "Self-custodial. Private keys are generated and stored locally; signing never leaves this device." })
      ]);
    }
    function aboutLine(k, v) { return el("div", { class: "about-line" }, [el("span", { text: k }), el("span", { class: "mono", text: v })]); }
  }

  global.Views.settings = { title: "Settings", render: render };
})(window);
