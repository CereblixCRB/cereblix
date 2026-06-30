/* ============================================================================
   views/history.js — transaction history, all addresses or filtered to one.
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, API = global.API, Store = global.Store, VC = global.VC, el = UI.el;
  global.Views = global.Views || {};

  function render(mount, params, ctx) {
    var current = params.address || "";

    var sel = el("select", { class: "select", style: "max-width:320px" }, addrOptions(current));
    ctx.setActions([
      el("button", { class: "btn btn-ghost btn-icon", title: "Refresh", "aria-label": "Refresh", html: UI.icon("refresh", 18), onclick: function () { load(); } })
    ]);

    var listHost = el("div");
    var card = el("div", { class: "card card-pad" }, [
      el("div", { class: "card-head" }, [
        el("span", { html: UI.icon("history", 18), style: "color:var(--accent)" }),
        el("h2", { text: "Transactions" }), el("div", { class: "spacer" }), sel
      ]),
      listHost
    ]);
    mount.appendChild(card);

    sel.addEventListener("change", function () { current = sel.value; load(); });

    function load() {
      UI.clear(listHost); listHost.appendChild(UI.skeletonRows(5));
      API.History(current).then(function (items) {
        UI.clear(listHost);
        if (!items || !items.length) {
          listHost.appendChild(UI.empty("➜", "No transactions", current ? "This address has no activity yet." : "Your sent and received CRB will appear here."));
          return;
        }
        var ref = current && /^crb1/.test(current) ? current : null; // direction reference if a single address is selected
        // if a label was selected, resolve to its address for direction
        if (current && !ref) { var a = (Store.state.addresses || []).filter(function (x) { return x.Label === current || x.Addr === current; })[0]; if (a) ref = a.Addr; }
        items.forEach(function (it) { listHost.appendChild(VC.txRow(it, ref)); });
        listHost.appendChild(el("div", { class: "faint center-text", style: "padding:14px;font-size:12px", text: "Showing " + items.length + " transaction" + (items.length === 1 ? "" : "s") }));
      }).catch(function (e) { UI.clear(listHost); listHost.appendChild(UI.banner("Could not load history: " + e.message, "err")); });
    }

    function addrOptions(selected) {
      var opts = [el("option", { value: "", selected: !selected, text: "All addresses" })];
      (Store.state.addresses || []).forEach(function (a) {
        opts.push(el("option", { value: a.Addr, selected: selected === a.Addr, text: (a.Label || "Address") + " · " + UI.shortMid(a.Addr, 8, 6) }));
      });
      return opts;
    }

    load();
    ctx.onLeave(Store.on("addresses", function () {
      var keep = sel.value; UI.clear(sel); UI.append(sel, addrOptions(keep));
    }));
  }

  global.Views.history = { title: "History", render: render };
})(window);
