/* ============================================================================
   views/receive.js — show a wallet address as a QR (inline generator) + copy.
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, API = global.API, Store = global.Store, VC = global.VC, Router = global.Router, QR = global.QR, el = UI.el;
  global.Views = global.Views || {};

  function render(mount, params, ctx) {
    ctx.setActions([
      el("button", { class: "btn btn-sm", html: UI.icon("plus", 16) + "<span>New address</span>", onclick: addAddress })
    ]);

    var list = Store.state.addresses || [];
    if (!list.length) {
      mount.appendChild(UI.empty("✦", "No addresses yet", "Create an address to receive CRB.",
        el("button", { class: "btn btn-primary", style: "margin-top:8px", text: "Create address", onclick: addAddress })));
      return;
    }

    var selected = params.address && list.filter(function (a) { return a.Addr === params.address; })[0] || list[0];

    var qrHost = el("div", { class: "qr-wrap" });
    var listHost = el("div");

    var layout = el("div", { class: "grid grid-2" }, [
      el("div", { class: "card card-pad" }, [el("h2", { class: "card-title", text: "Receive CRB" }), qrHost]),
      el("div", { class: "card card-pad" }, [
        el("div", { class: "card-head" }, [el("h2", { text: "Your addresses" }), el("div", { class: "spacer" }),
          el("button", { class: "btn btn-sm btn-ghost", html: UI.icon("plus", 15) + "<span>Add</span>", onclick: addAddress })]),
        listHost
      ])
    ]);
    mount.appendChild(layout);

    function renderQR() {
      UI.clear(qrHost);
      var card;
      try { card = QR.toElement(selected.Addr, { ecl: "M", border: 3 }); }
      catch (e) { card = UI.banner("Could not render QR: " + e.message, "err"); }
      UI.append(qrHost, [
        card,
        el("div", { style: "width:100%;max-width:340px" }, [
          el("div", { class: "center-text", style: "font-family:var(--fd);font-weight:600;margin-bottom:8px", text: selected.Label || "Address" }),
          UI.copyable(selected.Addr, { label: "Address", display: selected.Addr, class: "copyable-block" })
        ]),
        el("div", { class: "btn-row", style: "justify-content:center" }, [
          el("button", { class: "btn btn-primary", html: UI.icon("copy", 16) + "<span>Copy address</span>", onclick: function () { UI.copy(selected.Addr, "Address"); } }),
          el("button", { class: "btn", html: UI.icon("send", 16) + "<span>Request via send</span>", onclick: function () { Router.go("send", { to: selected.Addr }); } })
        ])
      ]);
    }

    function renderList() {
      UI.clear(listHost);
      (Store.state.addresses || []).forEach(function (a) {
        var row = VC.addrRow(a, { you: false, onclick: function () { selected = a; renderQR(); markActive(); } });
        row.dataset.addr = a.Addr;
        listHost.appendChild(row);
      });
      markActive();
    }
    function markActive() {
      UI.$$(".addr-row", listHost).forEach(function (r) {
        r.classList.toggle("active-row", r.dataset.addr === selected.Addr);
      });
    }

    renderQR(); renderList();
    ctx.onLeave(Store.on("addresses", function () { renderList(); }));

    function addAddress() {
      var input = el("input", { class: "input", placeholder: "Label (optional)", maxlength: "40", autofocus: true });
      var err = el("div");
      var m = UI.modal({
        title: "New address",
        body: [err, el("div", { class: "field", style: "margin:0" }, [el("label", { text: "Label" }), input,
          el("div", { class: "hint", text: "A fresh ed25519 key pair is generated locally." })])],
        footer: [
          el("button", { class: "btn btn-ghost", text: "Cancel", onclick: function () { m.close(); } }),
          el("button", { class: "btn btn-primary", text: "Create", onclick: create })
        ]
      });
      input.addEventListener("keydown", function (e) { if (e.key === "Enter") create(); });
      function create() {
        UI.clear(err);
        API.CreateAddress(input.value.trim()).then(function (k) {
          return Store.refreshAddresses().then(function () {
            m.close(); UI.toast("Address created", "ok");
            selected = (Store.state.addresses || []).filter(function (a) { return a.Addr === k.Addr; })[0] || selected;
            renderQR();
          });
        }).catch(function (e) { err.appendChild(UI.banner(e.message, "err")); });
      }
    }
  }

  global.Views.receive = { title: "Receive", render: render };
})(window);
