/* ============================================================================
   views/common.js — shared renderers used across screens.
   Exposed as window.VC (view-commons).
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, Store = global.Store, Router = global.Router, el = UI.el;

  function isMine(addr, ref) { return ref ? addr === ref : Store.isOurs(addr); }

  // direction of a history/tx item relative to a reference address (or our set)
  function direction(item, ref) {
    if (item.from === "coinbase" || item.from === "") return "in";
    var fromMine = isMine(item.from, ref), toMine = isMine(item.to, ref);
    if (fromMine && !toMine) return "out";
    if (toMine && !fromMine) return "in";
    if (fromMine && toMine) return "self";
    return toMine ? "in" : "out";
  }

  function confLabel(height) {
    if (!height) return null;
    var h = Store.height();
    if (!h || h < height) return { n: 0, text: "pending" };
    var n = h - Number(height) + 1;
    return { n: n, text: n <= 0 ? "pending" : (n === 1 ? "1 conf" : (n >= 100 ? "100+ conf" : n + " conf")) };
  }

  function amountText(syn, dir) {
    var s = UI.synToCrb(syn);
    if (dir === "in") return "+" + s;
    if (dir === "out") return "−" + s;
    return s;
  }

  // a single transaction row (history / dashboard / explorer)
  function txRow(item, ref) {
    var dir = direction(item, ref);
    var coinbase = item.from === "coinbase" || item.from === "";
    var peer = coinbase ? "Mining reward" : (dir === "out" ? item.to : item.from);
    var conf = confLabel(item.height);
    var icName = dir === "out" ? "send" : "receive";
    var row = el("div", { class: "tx-row", role: "button", tabindex: "0", title: "View transaction" }, [
      el("div", { class: "tx-ic " + (dir === "self" ? "" : dir), html: UI.icon(coinbase ? "coins" : icName, 18) }),
      el("div", { class: "tx-main" }, [
        el("div", { class: "l1" }, [
          el("span", { text: coinbase ? "Mined block reward" : (dir === "out" ? "Sent" : (dir === "in" ? "Received" : "Internal transfer")) }),
          conf ? el("span", { class: "tag " + (conf.n <= 0 ? "pend" : ""), text: conf.text }) : null
        ]),
        el("div", { class: "l2", title: peer, text: coinbase ? "Coinbase" : UI.shortMid(peer, 10, 8) })
      ]),
      el("div", { class: "tx-amt " + (dir === "in" ? "pos" : (dir === "out" ? "neg" : "")) }, [
        el("div", { text: amountText(item.amount, dir) + " CRB" }),
        el("div", { class: "sub", text: UI.relTime(item.time) })
      ])
    ]);
    function open() { Router.go("explorer", { q: item.txid }); }
    row.addEventListener("click", open);
    row.addEventListener("keydown", function (e) { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } });
    return row;
  }

  // a wallet address row (dashboard / addresses / receive picker)
  function addrRow(a, opts) {
    opts = opts || {};
    var info = el("div", { class: "info" }, [
      el("div", { class: "label" }, [el("span", { text: a.Label || "Address" }), opts.you ? el("span", { class: "tag you", text: "You" }) : null]),
      el("div", { class: "addr", title: a.Addr, text: a.Addr })
    ]);
    var children = [UI.avatar(a.Addr), info];
    if (a.Balance != null) children.push(el("div", { class: "bal" }, [UI.prettyCrb(a.Balance), " ", el("small", { text: "CRB" })]));
    if (opts.actions) children.push(el("div", { class: "row-actions" }, opts.actions));
    var row = el("div", { class: "addr-row" }, children);
    if (opts.onclick) {
      row.setAttribute("role", "button"); row.setAttribute("tabindex", "0");
      row.style.cursor = "pointer";
      row.addEventListener("click", function (e) { if (e.target.closest(".row-actions")) return; opts.onclick(a); });
      row.addEventListener("keydown", function (e) { if (e.key === "Enter") opts.onclick(a); });
    }
    return row;
  }

  // network status chips
  function statusChips(s) {
    if (!s) return el("div", { class: "chips" }, [chip("Network", "offline")]);
    return el("div", { class: "chips" }, [
      chip("Height", UI.num(s.height)),
      chip("Supply", UI.synToCrb(s.supply), "CRB"),
      chip("Hashrate", UI.hashrate(s.hashrate)),
      chip("Block reward", UI.synToCrb(s.reward), "CRB"),
      chip("Peers", UI.num(s.peers)),
      chip("Mempool", UI.num(s.mempool), "txns")
    ]);
  }
  function chip(k, v, unit) {
    return el("div", { class: "chip" }, [
      el("div", { class: "k", text: k }),
      el("div", { class: "v" }, [String(v), unit ? el("small", { text: " " + unit }) : null])
    ]);
  }

  global.VC = {
    direction: direction, confLabel: confLabel, amountText: amountText,
    txRow: txRow, addrRow: addrRow, statusChips: statusChips, chip: chip
  };
})(window);
