/* ============================================================================
   views/explorer.js — search blocks / txs / addresses, network status, richlist.
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, API = global.API, Store = global.Store, VC = global.VC, Router = global.Router, el = UI.el;
  global.Views = global.Views || {};

  function render(mount, params, ctx) {
    var input = el("input", { class: "input mono", placeholder: "Search address, block height/hash, or txid", value: params.q || "", autocomplete: "off", spellcheck: "false" });
    var form = el("form", { class: "input-with-btn", style: "max-width:640px;margin:0 auto 24px", onsubmit: function (e) { e.preventDefault(); Router.go("explorer", { q: input.value.trim() }); } }, [
      input,
      el("button", { class: "btn btn-primary", type: "submit", html: UI.icon("search", 16) + "<span>Search</span>" })
    ]);
    mount.appendChild(form);

    var results = el("div");
    mount.appendChild(results);

    if (params.q) doSearch(params.q.trim());
    else overview();

    setTimeout(function () { if (!params.q) input.focus(); }, 30);

    function loadingState() { UI.clear(results); results.appendChild(UI.loading("Searching…")); }
    function errState(msg) { UI.clear(results); results.appendChild(UI.banner(msg, "err")); results.appendChild(backLink()); }
    function backLink() { return el("button", { class: "muted-link", style: "background:none;border:none;font:inherit;margin-top:16px", html: UI.icon("chevronR", 14) + " Back to network overview", onclick: function () { Router.go("explorer"); } }); }
    function addrLink(a) {
      var node = el("a", { class: "mono", text: UI.shortMid(a, 10, 8), title: a, onclick: function () { Router.go("explorer", { q: a }); } });
      return Store.isOurs(a) ? el("span", {}, [node, " ", el("span", { class: "tag you", text: "You" })]) : node;
    }

    // ----------------------------- search -----------------------------
    function doSearch(q) {
      loadingState();
      API.Search(q).then(function (r) {
        if (r.type === "address") return renderAddress(r.value);
        if (r.type === "block") return renderBlock(String(r.height != null ? r.height : q));
        if (r.type === "tx") return renderTx(r.value);
        errState("Unrecognized result.");
      }).catch(function (e) { errState(e.message || "Nothing found for that query."); });
    }

    // ----------------------------- address -----------------------------
    function renderAddress(addr) {
      API.AddressInfo(addr).then(function (a) {
        UI.clear(results);
        var ours = Store.isOurs(addr);
        var kv = el("dl", { class: "kv" }, [
          dt("Address"), dd(UI.copyable(addr, { label: "Address", display: addr })),
          dt("Balance"), dd(UI.synToCrb(a.balance) + " CRB"),
          dt("Spendable"), dd(UI.synToCrb(a.spendable != null ? a.spendable : a.balance) + " CRB"),
          dt("Nonce"), dd(String(a.nonce != null ? a.nonce : 0)),
          a.received != null ? dt("Total received") : null, a.received != null ? dd(UI.synToCrb(a.received) + " CRB") : null,
          a.mined != null ? dt("Total mined") : null, a.mined != null ? dd(UI.synToCrb(a.mined) + " CRB") : null,
          a.sent != null ? dt("Total sent") : null, a.sent != null ? dd(UI.synToCrb(a.sent) + " CRB") : null,
          a.txn != null ? dt("Transactions") : null, a.txn != null ? dd(String(a.txn)) : null
        ]);
        results.appendChild(card("Address" + (ours ? "  ·  yours" : ""), "wallet", [
          kv,
          ours ? el("div", { style: "margin-top:18px" }, [el("button", { class: "btn btn-sm", html: UI.icon("history", 15) + "<span>View in History</span>", onclick: function () { Router.go("history", { address: addr }); } })]) : null
        ]));
        results.appendChild(backLink());
      }).catch(function (e) { errState(e.message); });
    }

    // ----------------------------- block -----------------------------
    function renderBlock(q) {
      API.GetBlock(q).then(function (b) {
        UI.clear(results);
        var txs = b.txs || [];
        var miner = txs[0] ? txs[0].to : "";
        var reward = txs[0] ? txs[0].amount : 0;
        var bh = b.hash || b.Hash || ""; // /api/block (core.Block) does not serialize the hash; show only if the backend supplies it
        var kv = el("dl", { class: "kv" }, [
          dt("Height"), dd(String(b.height)),
          bh ? dt("Hash") : null, bh ? dd(UI.copyable(bh, { label: "Block hash", display: UI.shortMid(bh, 14, 12) })) : null,
          dt("Time"), dd(UI.absTime(b.time) + "  (" + UI.relTime(b.time) + ")"),
          dt("Transactions"), dd(String(txs.length)),
          dt("Miner"), dd(miner ? addrLink(miner) : "—"),
          dt("Reward"), dd(UI.synToCrb(reward) + " CRB"),
          dt("Nonce"), dd(String(b.nonce)),
          dt("Previous"), dd(b.prev ? el("a", { class: "mono", text: UI.shortMid(b.prev, 12, 10), onclick: function () { Router.go("explorer", { q: b.prev }); } }) : "—")
        ]);
        var nav = el("div", { class: "btn-row", style: "margin:0 0 16px" }, [
          el("button", { class: "btn btn-sm btn-ghost", disabled: Number(b.height) <= 0, html: UI.icon("chevronR", 14) + "<span>Prev block</span>", onclick: function () { Router.go("explorer", { q: String(Number(b.height) - 1) }); } }),
          el("button", { class: "btn btn-sm btn-ghost", html: "<span>Next block</span>" + UI.icon("arrowR", 14), onclick: function () { Router.go("explorer", { q: String(Number(b.height) + 1) }); } })
        ]);
        results.appendChild(card("Block #" + b.height, "explorer", [nav, kv, txTable(txs)]));
        results.appendChild(backLink());
      }).catch(function (e) { errState(e.message); });
    }

    function txTable(txs) {
      if (!txs.length) return null;
      var rows = txs.map(function (t) {
        var cb = !t.from_pub;
        return el("tr", {}, [
          el("td", {}, [cb ? el("span", { class: "tag in", text: "coinbase" }) : el("span", { class: "tag", text: "tx" })]),
          el("td", {}, [cb ? "—" : addrLinkFromPub(t)]),
          el("td", {}, [addrLink(t.to)]),
          el("td", { class: "num" }, [UI.synToCrb(t.amount)]),
          el("td", { class: "num dim" }, [cb ? "—" : UI.synToCrb(t.fee)])
        ]);
      });
      return el("div", { style: "margin-top:18px;overflow-x:auto" }, [
        el("div", { class: "section-label", style: "margin-top:0", text: "Transactions" }),
        el("table", { class: "tbl" }, [
          el("thead", {}, [el("tr", {}, [th("Type"), th("From"), th("To"), thNum("Amount"), thNum("Fee")])]),
          el("tbody", {}, rows)
        ])
      ]);
    }
    function addrLinkFromPub(t) {
      // we don't derive the address client-side; show the pubkey short, non-link
      return el("span", { class: "mono dim", title: t.from_pub, text: UI.shortMid(t.from_pub, 8, 6) });
    }

    // ----------------------------- tx -----------------------------
    function renderTx(txid) {
      API.GetTx(txid).then(function (t) {
        UI.clear(results);
        var cb = t.coinbase || t.from === "coinbase";
        var kv = el("dl", { class: "kv" }, [
          dt("Transaction"), dd(UI.copyable(t.txid || txid, { label: "Transaction ID", display: UI.shortMid(t.txid || txid, 14, 12) })),
          dt("Status"), dd(t.pending ? el("span", { class: "tag pend", text: "Pending (mempool)" }) : el("span", { class: "tag in", text: "Confirmed" })),
          !t.pending ? dt("Block") : null, !t.pending ? dd(el("a", { text: "#" + t.height, onclick: function () { Router.go("explorer", { q: String(t.height) }); } })) : null,
          t.block_hash ? dt("Block hash") : null, t.block_hash ? dd(UI.shortMid(t.block_hash, 12, 10)) : null,
          dt("Time"), dd(t.time ? UI.absTime(t.time) + "  (" + UI.relTime(t.time) + ")" : "—"),
          dt("From"), dd(cb ? el("span", { class: "tag in", text: "coinbase" }) : addrLink(t.from)),
          dt("To"), dd(addrLink(t.to)),
          dt("Amount"), dd(UI.synToCrb(t.amount) + " CRB"),
          dt("Fee"), dd(cb ? "—" : UI.synToCrb(t.fee) + " CRB"),
          dt("Nonce"), dd(String(t.nonce != null ? t.nonce : 0))
        ]);
        results.appendChild(card("Transaction", "send", [kv]));
        results.appendChild(backLink());
      }).catch(function (e) { errState(e.message); });
    }

    // ----------------------------- overview -----------------------------
    function overview() {
      UI.clear(results);
      var statusHost = el("div", { class: "card card-pad" }, [
        el("div", { class: "card-head" }, [el("span", { html: UI.icon("node", 18), style: "color:var(--accent)" }), el("h2", { text: "Network status" }), el("div", { class: "spacer" }),
          el("button", { class: "btn btn-sm btn-ghost", title: "Refresh", html: UI.icon("refresh", 15), onclick: function () { Store.refreshStatus().then(fillStatus).catch(function () {}); } })]),
        el("div", { id: "exp-status" })
      ]);
      var memHost = el("div", { class: "card card-pad" }, [
        el("div", { class: "card-head" }, [el("span", { html: UI.icon("history", 18), style: "color:var(--accent)" }), el("h2", { text: "Pending transactions" }), el("div", { class: "spacer" }),
          el("button", { class: "btn btn-sm btn-ghost", title: "Refresh", html: UI.icon("refresh", 15), onclick: loadMempool })]),
        el("div", { id: "exp-mem" })
      ]);
      var richHost = el("div", { class: "card card-pad" }, [
        el("div", { class: "card-head" }, [el("span", { html: UI.icon("coins", 18), style: "color:var(--accent)" }), el("h2", { text: "Rich list" }), el("div", { class: "spacer" })]),
        el("div", { id: "exp-rich" })
      ]);
      results.appendChild(statusHost);
      results.appendChild(el("div", { style: "height:20px" }));
      results.appendChild(memHost);
      results.appendChild(el("div", { style: "height:20px" }));
      results.appendChild(richHost);

      fillStatus(); loadMempool(); loadRich();
      ctx.onLeave(Store.on("status", fillStatus));
    }

    function loadMempool() {
      var host = UI.$("#exp-mem"); if (!host) return;
      UI.clear(host); host.appendChild(UI.skeletonRows(2));
      API.Mempool().then(function (txs) {
        UI.clear(host);
        if (!txs || !txs.length) { host.appendChild(el("div", { class: "dim", style: "padding:8px 0;font-size:13px", text: "Mempool is empty — no unconfirmed transactions." })); return; }
        var rows = txs.map(function (t) {
          return el("tr", {}, [
            el("td", {}, [el("span", { class: "mono dim", title: t.from_pub, text: t.from_pub ? UI.shortMid(t.from_pub, 8, 6) : "—" })]),
            el("td", {}, [addrLink(t.to)]),
            el("td", { class: "num" }, [UI.synToCrb(t.amount)]),
            el("td", { class: "num dim" }, [UI.synToCrb(t.fee)]),
            el("td", { class: "num dim" }, [String(t.nonce != null ? t.nonce : 0)])
          ]);
        });
        host.appendChild(el("div", { style: "overflow-x:auto" }, [el("table", { class: "tbl" }, [
          el("thead", {}, [el("tr", {}, [th("From"), th("To"), thNum("Amount"), thNum("Fee"), thNum("Nonce")])]),
          el("tbody", {}, rows)
        ])]));
      }).catch(function (e) { UI.clear(host); host.appendChild(UI.banner("Could not load mempool: " + e.message, "err")); });
    }

    function fillStatus() {
      var host = UI.$("#exp-status"); if (!host) return;
      var s = Store.state.status;
      UI.clear(host);
      if (!s) { host.appendChild(UI.banner("Node unreachable. Check the connection in Settings.", "warn")); return; }
      host.appendChild(VC.statusChips(s));
      host.appendChild(el("dl", { class: "kv", style: "margin-top:20px" }, [
        dt("Tip hash"), dd(s.tip ? UI.copyable(s.tip, { display: UI.shortMid(s.tip, 14, 12) }) : "—"),
        dt("Difficulty"), dd(s.difficulty != null ? UI.groupInt(String(s.difficulty)) : "—"),
        dt("Epoch"), dd(String(s.epoch != null ? s.epoch : "—")),
        dt("Suggested fee"), dd(s.fee_suggested != null ? UI.synToCrb(s.fee_suggested) + " CRB" : "—"),
        s.node_version ? dt("Node version") : null, s.node_version ? dd("v" + s.node_version + (s.consensus_version != null ? "  ·  consensus v" + s.consensus_version : "")) : null
      ]));
    }

    function loadRich() {
      var host = UI.$("#exp-rich"); if (!host) return;
      host.appendChild(UI.skeletonRows(4));
      API.Richlist(25).then(function (list) {
        UI.clear(host);
        if (!list || !list.length) { host.appendChild(UI.empty("✦", "No data", "The rich list is empty.")); return; }
        var total = list.reduce(function (s, e) { return s + Number(e.balance); }, 0) || 1;
        var rows = list.map(function (e, i) {
          return el("tr", {}, [
            el("td", { class: "dim", text: "#" + (i + 1) }),
            el("td", {}, [addrLink(e.address)]),
            el("td", { class: "num" }, [UI.synToCrb(e.balance)]),
            el("td", { class: "num dim" }, [((Number(e.balance) / total) * 100).toFixed(2) + "%"])
          ]);
        });
        host.appendChild(el("div", { style: "overflow-x:auto" }, [el("table", { class: "tbl" }, [
          el("thead", {}, [el("tr", {}, [th("Rank"), th("Address"), thNum("Balance (CRB)"), thNum("Share")])]),
          el("tbody", {}, rows)
        ])]));
      }).catch(function (e) { UI.clear(host); host.appendChild(UI.banner("Could not load rich list: " + e.message, "err")); });
    }

    // small helpers
    function card(title, ic, body) {
      return el("div", { class: "card card-pad" }, [
        el("div", { class: "card-head" }, [el("span", { html: UI.icon(ic, 18), style: "color:var(--accent)" }), el("h2", { text: title })]),
        el("div", {}, body)
      ]);
    }
    function dt(t) { return el("dt", { text: t }); }
    function dd(c) { return el("dd", typeof c === "string" ? { text: c } : null, typeof c === "string" ? null : c); }
    function th(t) { return el("th", { text: t }); }
    function thNum(t) { return el("th", { class: "num", text: t }); }
  }

  global.Views.explorer = { title: "Explorer", render: render };
})(window);
