/* ============================================================================
   views/dashboard.js — total balance, network chips, addresses, recent activity.
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, API = global.API, Store = global.Store, VC = global.VC, Router = global.Router, el = UI.el;
  global.Views = global.Views || {};

  function render(mount, params, ctx) {
    ctx.setActions([
      el("button", { class: "btn btn-ghost btn-icon", title: "Refresh", "aria-label": "Refresh", html: UI.icon("refresh", 18),
        onclick: function () { reloadAll(); } })
    ]);

    var chipsBox = el("div");
    var addrBox = el("div");
    var actBox = el("div");

    var hero = el("div", { class: "hero" }, [
      el("div", { class: "label", text: "Total balance" }),
      el("div", { class: "amount" }, [el("span", { id: "dash-total", text: UI.prettyCrb(Store.state.total) }), el("span", { class: "ticker", text: "CRB" })]),
      el("div", { class: "hero-actions" }, [
        el("button", { class: "btn btn-primary", html: UI.icon("send", 17) + "<span>Send</span>", onclick: function () { Router.go("send"); } }),
        el("button", { class: "btn", html: UI.icon("receive", 17) + "<span>Receive</span>", onclick: function () { Router.go("receive"); } })
      ]),
      chipsBox
    ]);

    var addrCard = el("div", { class: "card card-pad" }, [
      el("div", { class: "card-head" }, [
        el("h2", { text: "Your addresses" }), el("div", { class: "spacer" }),
        el("button", { class: "btn btn-sm btn-ghost", html: UI.icon("arrowR", 15) + "<span>Manage</span>", onclick: function () { Router.go("addresses"); } })
      ]),
      addrBox
    ]);

    var actCard = el("div", { class: "card card-pad" }, [
      el("div", { class: "card-head" }, [
        el("h2", { text: "Recent activity" }), el("div", { class: "spacer" }),
        el("button", { class: "btn btn-sm btn-ghost", html: UI.icon("arrowR", 15) + "<span>View all</span>", onclick: function () { Router.go("history"); } })
      ]),
      actBox
    ]);

    UI.append(mount, [hero, el("div", { class: "grid grid-2", style: "margin-top:20px" }, [addrCard, actCard])]);

    function renderChips() { UI.clear(chipsBox); chipsBox.appendChild(VC.statusChips(Store.state.status)); }
    function renderTotal() { var t = UI.$("#dash-total"); if (t) t.textContent = UI.prettyCrb(Store.state.total); }
    function renderAddrs() {
      UI.clear(addrBox);
      var list = Store.state.addresses || [];
      if (!list.length) { addrBox.appendChild(UI.empty("✦", "No addresses", "Create one to start receiving CRB.")); return; }
      list.slice(0, 4).forEach(function (a) {
        addrBox.appendChild(VC.addrRow(a, { you: true, onclick: function () { Router.go("receive", { address: a.Addr }); }, actions: [
          el("button", { class: "btn btn-sm btn-ghost", title: "Receive", "aria-label": "Receive to " + a.Label, html: UI.icon("qr", 15), onclick: function () { Router.go("receive", { address: a.Addr }); } })
        ] }));
      });
      if (list.length > 4) addrBox.appendChild(el("button", { class: "muted-link", style: "display:block;margin:12px auto 0;background:none;border:none;font:inherit", text: "+ " + (list.length - 4) + " more", onclick: function () { Router.go("addresses"); } }));
    }

    actBox.appendChild(UI.skeletonRows(3));
    function loadActivity() {
      API.History("").then(function (items) {
        UI.clear(actBox);
        if (!items || !items.length) { actBox.appendChild(UI.empty("➜", "No transactions yet", "Received and sent CRB will appear here.")); return; }
        items.slice(0, 6).forEach(function (it) { actBox.appendChild(VC.txRow(it, null)); });
      }).catch(function (e) { UI.clear(actBox); actBox.appendChild(UI.banner("Could not load activity: " + e.message, "err")); });
    }

    function reloadAll() {
      Store.refreshAddresses().then(renderAddrs).then(renderTotal).catch(function () {});
      Store.refreshStatus().then(renderChips).catch(renderChips);
      loadActivity();
    }

    renderChips(); renderAddrs(); renderTotal(); loadActivity();

    // live updates
    ctx.onLeave(Store.on("status", renderChips));
    ctx.onLeave(Store.on("addresses", function () { renderAddrs(); renderTotal(); }));
  }

  global.Views.dashboard = { title: "Dashboard", render: render };
})(window);
