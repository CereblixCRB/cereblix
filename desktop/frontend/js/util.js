/* ============================================================================
   util.js — DOM helpers, formatting, icons, toasts, modals, clipboard.
   Exposed as window.UI.
   ========================================================================== */
(function (global) {
  "use strict";

  // ---------- DOM ----------
  function el(tag, props, children) {
    var node = document.createElement(tag);
    if (props) {
      for (var k in props) {
        if (!Object.prototype.hasOwnProperty.call(props, k)) continue;
        var v = props[k];
        if (v == null || v === false) continue;
        if (k === "class" || k === "className") node.className = v;
        else if (k === "html") node.innerHTML = v;
        else if (k === "text") node.textContent = v;
        else if (k === "dataset") { for (var d in v) node.dataset[d] = v[d]; }
        else if (k.slice(0, 2) === "on" && typeof v === "function") node.addEventListener(k.slice(2).toLowerCase(), v);
        else if (k === "disabled" || k === "checked" || k === "selected" || k === "autofocus") { if (v) node.setAttribute(k, ""); }
        else node.setAttribute(k, v);
      }
    }
    appendChildren(node, children);
    return node;
  }
  function appendChildren(node, children) {
    if (children == null) return;
    if (!Array.isArray(children)) children = [children];
    children.forEach(function (c) {
      if (c == null || c === false) return;
      node.appendChild(typeof c === "string" || typeof c === "number" ? document.createTextNode(String(c)) : c);
    });
  }
  function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); return node; }
  function $(sel, root) { return (root || document).querySelector(sel); }
  function $$(sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); }

  function escapeHtml(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function debounce(fn, ms) {
    var t; return function () {
      var args = arguments, ctx = this;
      clearTimeout(t); t = setTimeout(function () { fn.apply(ctx, args); }, ms);
    };
  }

  // ---------- icons (inline stroke SVG) ----------
  var ICONS = {
    dashboard: "M3 12l9-8 9 8M5 10v9a1 1 0 001 1h4v-6h4v6h4a1 1 0 001-1v-9",
    send: "M5 19L19 5M9 5h10v10",
    receive: "M19 5L5 19M15 19H5V9",
    history: "M12 8v4l3 2M21 12a9 9 0 11-3.5-7.1M21 4v4h-4",
    wallet: "M3 7a2 2 0 012-2h12a2 2 0 012 2v1H5a2 2 0 010-4h12M3 7v10a2 2 0 002 2h13a2 2 0 002-2v-6H5M16 13h.01",
    explorer: "M11 19a8 8 0 100-16 8 8 0 000 16zM21 21l-4.3-4.3",
    settings: "M4 6h16M4 12h16M4 18h16M9 6v0M15 12v0M7 18v0",
    lock: "M6 11V8a6 6 0 0112 0v3M5 11h14a1 1 0 011 1v8a1 1 0 01-1 1H5a1 1 0 01-1-1v-8a1 1 0 011-1z",
    unlock: "M7 11V8a5 5 0 019.9-1M5 11h14a1 1 0 011 1v8a1 1 0 01-1 1H5a1 1 0 01-1-1v-8a1 1 0 011-1z",
    copy: "M9 9h10a1 1 0 011 1v10a1 1 0 01-1 1H9a1 1 0 01-1-1V10a1 1 0 011-1zM5 15H4a1 1 0 01-1-1V4a1 1 0 011-1h10a1 1 0 011 1v1",
    check: "M5 13l4 4L19 7",
    x: "M6 6l12 12M18 6L6 18",
    chevronR: "M9 6l6 6-6 6",
    chevronD: "M6 9l6 6 6-6",
    plus: "M12 5v14M5 12h14",
    key: "M15 7a4 4 0 11-3.8 5.2L4 19.4 6.6 22M9 16l2.2-2.2",
    import: "M12 3v12M8 11l4 4 4-4M5 21h14",
    export: "M12 21V9M8 13l4-4 4 4M5 3h14",
    warning: "M12 3l9 16H3L12 3zM12 9v5M12 17v0",
    info: "M12 21a9 9 0 100-18 9 9 0 000 18zM12 11v5M12 8v0",
    eye: "M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7zM12 15a3 3 0 100-6 3 3 0 000 6z",
    eyeOff: "M3 3l18 18M10.6 10.6A3 3 0 0014 14M9.9 5.1A9.8 9.8 0 0112 5c6.5 0 10 7 10 7a16 16 0 01-3 3.6M6.2 6.2A16 16 0 002 12s3.5 7 10 7a9.7 9.7 0 003.2-.5",
    refresh: "M21 12a9 9 0 11-2.6-6.3M21 4v5h-5",
    external: "M14 5h5v5M19 5l-9 9M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5",
    search: "M11 19a8 8 0 100-16 8 8 0 000 16zM21 21l-4.3-4.3",
    qr: "M4 4h6v6H4V4zM14 4h6v6h-6V4zM4 14h6v6H4v-6zM14 14h2v2h-2zM18 14h2v2h-2zM14 18h2v2h-2zM18 18h2v2h-2z",
    coins: "M8 14a6 3 0 100-6 6 3 0 000 6zM2 11v3c0 1.7 2.7 3 6 3s6-1.3 6-3v-3M14 7.5C14 5.8 11.3 4.5 8 4.5S2 5.8 2 7.5",
    node: "M12 3a3 3 0 100 6 3 3 0 000-6zM5 21a3 3 0 100-6 3 3 0 000 6zM19 21a3 3 0 100-6 3 3 0 000 6zM10.5 8L6.5 14.5M13.5 8l4 6.5",
    plug: "M9 2v6M15 2v6M7 8h10v3a5 5 0 01-10 0V8zM12 16v6",
    sparkle: "M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8L12 3z",
    arrowR: "M5 12h14M13 6l6 6-6 6",
    trash: "M4 7h16M9 7V5a1 1 0 011-1h4a1 1 0 011 1v2M6 7l1 13a1 1 0 001 1h8a1 1 0 001-1l1-13",
    edit: "M4 20h4L19 9l-4-4L4 16v4zM14 6l4 4"
  };
  function icon(name, size) {
    var d = ICONS[name] || ICONS.info;
    var s = size || 18;
    return '<svg class="ic" viewBox="0 0 24 24" width="' + s + '" height="' + s +
      '" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="' + d + '"/></svg>';
  }
  function iconEl(name, size) { var span = document.createElement("span"); span.style.display = "inline-flex"; span.innerHTML = icon(name, size); return span.firstChild; }

  // ---------- number / time formatting ----------
  function groupInt(intStr) {
    var neg = intStr[0] === "-"; if (neg) intStr = intStr.slice(1);
    return (neg ? "-" : "") + intStr.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  }
  // pretty-print a CRB *string* (already in CRB units): group + trim trailing zeros
  function prettyCrb(str) {
    str = String(str == null ? "0" : str).trim();
    if (str === "" || isNaN(Number(str))) return str || "0";
    var parts = str.split("."), ip = parts[0], fp = parts[1] || "";
    fp = fp.replace(/0+$/, "");
    return groupInt(ip) + (fp ? "." + fp : "");
  }
  // convert uint64 synapses (number/string) to a pretty CRB string (precise via BigInt)
  function synToCrb(syn) {
    var big;
    try { big = BigInt(typeof syn === "number" ? Math.trunc(syn) : (syn == null ? 0 : syn)); }
    catch (e) { return "0"; }
    var unit = 100000000n;
    var neg = big < 0n; if (neg) big = -big;
    var ip = (big / unit).toString();
    var fp = (big % unit).toString().padStart(8, "0").replace(/0+$/, "");
    return (neg ? "-" : "") + groupInt(ip) + (fp ? "." + fp : "");
  }
  function num(n) { return groupInt(String(Math.round(Number(n) || 0))); }

  function hashrate(hps) {
    hps = Number(hps) || 0;
    var u = ["H/s", "kH/s", "MH/s", "GH/s", "TH/s"], i = 0;
    while (hps >= 1000 && i < u.length - 1) { hps /= 1000; i++; }
    return (hps >= 100 ? hps.toFixed(0) : hps.toFixed(2)) + " " + u[i];
  }

  function relTime(sec) {
    sec = Number(sec) || 0;
    var d = Math.floor(Date.now() / 1000) - sec;
    if (d < 0) d = 0;
    if (d < 5) return "just now";
    if (d < 60) return d + "s ago";
    if (d < 3600) return Math.floor(d / 60) + "m ago";
    if (d < 86400) return Math.floor(d / 3600) + "h ago";
    if (d < 86400 * 30) return Math.floor(d / 86400) + "d ago";
    return absDate(sec);
  }
  function absTime(sec) { try { return new Date((Number(sec) || 0) * 1000).toLocaleString(); } catch (e) { return ""; } }
  function absDate(sec) { try { return new Date((Number(sec) || 0) * 1000).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }); } catch (e) { return ""; } }

  function shortMid(s, head, tail) {
    s = String(s || ""); head = head || 8; tail = tail || 6;
    return s.length <= head + tail + 1 ? s : s.slice(0, head) + "…" + s.slice(-tail);
  }

  // deterministic gradient avatar (data-URI-free, inline svg) for an address
  function avatar(seed, size) {
    size = size || 38;
    var h = 0; seed = String(seed || "");
    for (var i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) >>> 0;
    var a = h % 360, b = (h >> 3) % 360;
    var svg = '<svg width="' + size + '" height="' + size + '" viewBox="0 0 100 100" xmlns="http://www.w3.org/2000/svg" style="border-radius:11px">' +
      '<defs><linearGradient id="g' + h + '" x1="0" y1="0" x2="1" y2="1">' +
      '<stop offset="0" stop-color="hsl(' + a + ',70%,55%)"/><stop offset="1" stop-color="hsl(' + b + ',65%,42%)"/></linearGradient></defs>' +
      '<rect width="100" height="100" fill="url(#g' + h + ')"/>' +
      '<circle cx="' + (20 + h % 30) + '" cy="' + (25 + (h >> 4) % 30) + '" r="14" fill="rgba(255,255,255,.22)"/>' +
      '<circle cx="' + (70 - (h >> 6) % 30) + '" cy="' + (70 - (h >> 2) % 30) + '" r="20" fill="rgba(0,0,0,.16)"/></svg>';
    var span = document.createElement("span"); span.style.lineHeight = "0"; span.innerHTML = svg; return span.firstChild;
  }

  // ---------- clipboard ----------
  function copy(text, label) {
    function done() { toast((label || "Copied") + " to clipboard", "ok"); }
    function fail() { toast("Copy failed — select and copy manually", "err"); }
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, function () { legacyCopy(text) ? done() : fail(); });
        return;
      }
    } catch (e) {}
    legacyCopy(text) ? done() : fail();
  }
  function legacyCopy(text) {
    try {
      var ta = document.createElement("textarea");
      ta.value = text; ta.style.position = "fixed"; ta.style.opacity = "0";
      document.body.appendChild(ta); ta.focus(); ta.select();
      var ok = document.execCommand("copy");
      document.body.removeChild(ta); return ok;
    } catch (e) { return false; }
  }
  // build a clickable "copyable" element
  function copyable(text, opts) {
    opts = opts || {};
    var span = el("span", {
      class: "copyable " + (opts.class || ""), role: "button", tabindex: "0",
      title: "Click to copy", "aria-label": "Copy " + (opts.label || "value"),
      onclick: function () { copy(text, opts.label); }
    }, [
      el("span", { class: opts.mono === false ? "" : "mono", text: opts.display || text }),
      el("span", { class: "copy-ic", html: icon("copy", 14) })
    ]);
    span.addEventListener("keydown", function (e) { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); copy(text, opts.label); } });
    return span;
  }

  // ---------- toasts ----------
  function toastHost() {
    var h = $("#toasts");
    if (!h) { h = el("div", { id: "toasts", class: "toasts" }); document.body.appendChild(h); }
    return h;
  }
  function toast(msg, type, ms) {
    var t = el("div", { class: "toast " + (type || "") }, [
      el("span", { class: "bar" }),
      el("span", { text: msg })
    ]);
    toastHost().appendChild(t);
    setTimeout(function () {
      t.style.transition = "opacity .25s, transform .25s";
      t.style.opacity = "0"; t.style.transform = "translateX(20px)";
      setTimeout(function () { t.remove(); }, 260);
    }, ms || 3200);
  }

  // ---------- banners ----------
  function banner(msg, type, iconName) {
    return el("div", { class: "banner " + (type || "info") }, [
      el("span", { class: "ic", html: icon(iconName || (type === "err" ? "warning" : "info"), 16) }),
      el("span", typeof msg === "string" ? { text: msg } : null, typeof msg === "string" ? null : msg)
    ]);
  }

  // ---------- modal ----------
  var modalStack = [];
  function modal(opts) {
    opts = opts || {};
    var card = el("div", { class: "modal " + (opts.wide ? "wide" : ""), role: "dialog", "aria-modal": "true" });
    var head = el("div", { class: "modal-head" }, [
      el("h3", { id: "modal-title", text: opts.title || "" }),
      el("div", { class: "spacer" }),
      el("button", { class: "btn btn-ghost btn-icon", "aria-label": "Close", html: icon("x", 18), onclick: close })
    ]);
    if (opts.title) card.setAttribute("aria-labelledby", "modal-title");
    var body = el("div", { class: "modal-body" });
    appendChildren(body, opts.body);
    var foot = null;
    if (opts.footer) { foot = el("div", { class: "modal-foot" }); appendChildren(foot, opts.footer); }
    if (!opts.bare) card.appendChild(head);
    card.appendChild(body);
    if (foot) card.appendChild(foot);

    var backdrop = el("div", { class: "modal-backdrop" }, [card]);
    backdrop.addEventListener("mousedown", function (e) { if (e.target === backdrop && opts.dismissable !== false) close(); });
    var onKey = function (e) {
      if (e.key === "Escape" && opts.dismissable !== false) close();
      if (e.key === "Tab") trapFocus(card, e);
    };
    document.addEventListener("keydown", onKey);
    document.body.appendChild(backdrop);
    modalStack.push(backdrop);
    // focus first field
    setTimeout(function () {
      var f = card.querySelector("[autofocus]") || card.querySelector("input,button,select,textarea,[tabindex]");
      if (f) f.focus();
    }, 30);

    function close() {
      document.removeEventListener("keydown", onKey);
      backdrop.remove();
      modalStack = modalStack.filter(function (m) { return m !== backdrop; });
      if (opts.onClose) opts.onClose();
    }
    return { close: close, card: card, body: body };
  }
  function trapFocus(card, e) {
    var f = $$("a[href],button:not([disabled]),input:not([disabled]),select,textarea,[tabindex]:not([tabindex='-1'])", card)
      .filter(function (n) { return n.offsetParent !== null; });
    if (!f.length) return;
    var first = f[0], last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  }

  function confirm(opts) {
    return new Promise(function (resolve) {
      var m = modal({
        title: opts.title || "Confirm",
        body: typeof opts.message === "string" ? el("p", { class: "dim", text: opts.message }) : opts.message,
        footer: [
          el("button", { class: "btn btn-ghost", text: opts.cancelText || "Cancel", onclick: function () { m.close(); resolve(false); } }),
          el("button", { class: "btn " + (opts.danger ? "btn-danger" : "btn-primary"), text: opts.okText || "Confirm", onclick: function () { m.close(); resolve(true); } })
        ],
        onClose: function () { resolve(false); }
      });
    });
  }

  // ---------- misc UI builders ----------
  function skeletonRows(n) {
    var box = el("div", {});
    for (var i = 0; i < (n || 4); i++) box.appendChild(el("div", { class: "skel skel-row" }));
    return box;
  }
  function loading(text) {
    return el("div", { class: "loading-center" }, [el("div", { class: "spinner lg" }), el("div", { text: text || "Loading…" })]);
  }
  function empty(emoji, title, sub, action) {
    return el("div", { class: "empty" }, [
      el("div", { class: "emoji", text: emoji || "✦" }),
      el("h3", { text: title || "Nothing here yet" }),
      sub ? el("p", { class: "dim", text: sub }) : null,
      action || null
    ]);
  }

  global.UI = {
    el: el, clear: clear, append: appendChildren, $: $, $$: $$, escapeHtml: escapeHtml, debounce: debounce,
    icon: icon, iconEl: iconEl, avatar: avatar,
    prettyCrb: prettyCrb, synToCrb: synToCrb, num: num, hashrate: hashrate, groupInt: groupInt,
    relTime: relTime, absTime: absTime, absDate: absDate, shortMid: shortMid,
    copy: copy, copyable: copyable, toast: toast, banner: banner,
    modal: modal, confirm: confirm,
    skeletonRows: skeletonRows, loading: loading, empty: empty
  };
})(window);
