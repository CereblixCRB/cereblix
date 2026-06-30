/* ============================================================================
   views/addresses.js — manage addresses: create, import, export private key.
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, API = global.API, Store = global.Store, VC = global.VC, Router = global.Router, el = UI.el;
  global.Views = global.Views || {};

  function render(mount, params, ctx) {
    ctx.setActions([
      el("button", { class: "btn btn-sm btn-ghost", html: UI.icon("import", 16) + "<span>Import</span>", onclick: importKey }),
      el("button", { class: "btn btn-sm btn-primary", html: UI.icon("plus", 16) + "<span>New address</span>", onclick: createAddr })
    ]);

    var host = el("div");
    var card = el("div", { class: "card card-pad" }, [
      el("div", { class: "card-head" }, [el("h2", { text: "Addresses" }), el("div", { class: "spacer" }),
        el("span", { class: "dim", id: "addr-count" })]),
      host
    ]);
    mount.appendChild(card);

    function renderList() {
      UI.clear(host);
      var list = Store.state.addresses || [];
      var cnt = UI.$("#addr-count"); if (cnt) cnt.textContent = list.length + (list.length === 1 ? " address" : " addresses");
      if (!list.length) { host.appendChild(UI.empty("✦", "No addresses", "Create or import one to get started.")); return; }
      list.forEach(function (a) {
        host.appendChild(VC.addrRow(a, {
          you: false,
          actions: [
            el("button", { class: "btn btn-sm btn-ghost", title: "Copy address", "aria-label": "Copy", html: UI.icon("copy", 15), onclick: function () { UI.copy(a.Addr, "Address"); } }),
            el("button", { class: "btn btn-sm btn-ghost", title: "Receive", "aria-label": "Receive", html: UI.icon("qr", 15), onclick: function () { Router.go("receive", { address: a.Addr }); } }),
            el("button", { class: "btn btn-sm btn-ghost", title: "Export key", "aria-label": "Export private key", html: UI.icon("key", 15), onclick: function () { exportKey(a); } })
          ]
        }));
      });
    }
    renderList();
    ctx.onLeave(Store.on("addresses", renderList));

    // ----- create -----
    function createAddr() {
      var input = el("input", { class: "input", placeholder: "Label (optional)", maxlength: "40", autofocus: true });
      var err = el("div");
      var m = UI.modal({
        title: "New address",
        body: [err, el("div", { class: "field", style: "margin:0" }, [el("label", { text: "Label" }), input,
          el("div", { class: "hint", text: "Generates a new ed25519 key locally and adds it to your wallet." })])],
        footer: [el("button", { class: "btn btn-ghost", text: "Cancel", onclick: function () { m.close(); } }),
          el("button", { class: "btn btn-primary", text: "Create", onclick: go })]
      });
      input.addEventListener("keydown", function (e) { if (e.key === "Enter") go(); });
      function go() {
        UI.clear(err);
        API.CreateAddress(input.value.trim()).then(function () { return Store.refreshAddresses(); })
          .then(function () { m.close(); UI.toast("Address created", "ok"); })
          .catch(function (e) { err.appendChild(UI.banner(e.message, "err")); });
      }
    }

    // ----- import -----
    function importKey() {
      var key = el("textarea", { class: "input mono", rows: "3", placeholder: "128 hex characters", spellcheck: "false", autocomplete: "off", autofocus: true });
      var label = el("input", { class: "input", placeholder: "Label (optional)", maxlength: "40" });
      var err = el("div");
      var m = UI.modal({
        title: "Import private key",
        body: [err,
          el("div", { class: "field" }, [el("label", { text: "Private key" }), key, el("div", { class: "hint", text: "128-hex ed25519 key. Stays on this device." })]),
          el("div", { class: "field", style: "margin:0" }, [el("label", { text: "Label" }), label])],
        footer: [el("button", { class: "btn btn-ghost", text: "Cancel", onclick: function () { m.close(); } }),
          el("button", { class: "btn btn-primary", text: "Import", onclick: go })]
      });
      function go() {
        UI.clear(err);
        var k = key.value.trim().toLowerCase();
        if (!/^[0-9a-f]{128}$/.test(k)) { err.appendChild(UI.banner("Private key must be 128 hexadecimal characters.", "err")); return; }
        API.ImportKey(k, label.value.trim()).then(function () { return Store.refreshAddresses(); })
          .then(function () { m.close(); UI.toast("Key imported", "ok"); })
          .catch(function (e) { err.appendChild(UI.banner(e.message, "err")); });
      }
    }

    // ----- export (with warning) -----
    function exportKey(a) {
      var encrypted = Store.state.wallet && Store.state.wallet.Encrypted;
      var err = el("div");
      var passInput = encrypted ? el("input", { class: "input", type: "password", placeholder: "Wallet passphrase", autocomplete: "current-password", autofocus: true }) : null;
      var revealHost = el("div");
      var revealed = false;

      var revealBtn = el("button", { class: "btn btn-danger", text: "Reveal private key" });
      var m = UI.modal({
        title: "Export private key",
        wide: true,
        body: [
          UI.banner("Anyone with this key controls the funds at " + UI.shortMid(a.Addr, 8, 6) + ". Never share it, never paste it into a website.", "warn"),
          err,
          encrypted ? el("div", { class: "field" }, [el("label", { text: "Confirm passphrase" }), passInput]) : null,
          revealHost
        ],
        footer: [el("button", { class: "btn btn-ghost", text: "Close", onclick: function () { m.close(); } }), revealBtn]
      });

      revealBtn.addEventListener("click", function () {
        if (revealed) return;
        UI.clear(err);
        revealBtn.disabled = true; revealBtn.innerHTML = '<span class="spinner"></span> Decrypting…';
        API.ExportKey(a.Addr, passInput ? passInput.value : "").then(function (priv) {
          revealed = true;
          revealBtn.remove();
          var box = el("div", { class: "reveal-box blur", text: priv });
          var toggle = el("button", { class: "btn btn-sm btn-ghost", html: UI.icon("eye", 15) + "<span>Show</span>" });
          toggle.addEventListener("click", function () {
            var b = box.classList.toggle("blur");
            toggle.innerHTML = (b ? UI.icon("eye", 15) + "<span>Show</span>" : UI.icon("eyeOff", 15) + "<span>Hide</span>");
          });
          UI.append(revealHost, [
            el("div", { class: "field", style: "margin-top:8px" }, [
              el("label", { text: a.Label || "Private key" }), box,
              el("div", { class: "btn-row", style: "margin-top:10px" }, [
                toggle,
                el("button", { class: "btn btn-sm btn-primary", html: UI.icon("copy", 15) + "<span>Copy key</span>", onclick: function () { UI.copy(priv, "Private key"); } })
              ])
            ])
          ]);
        }).catch(function (e) {
          revealBtn.disabled = false; revealBtn.textContent = "Reveal private key";
          err.appendChild(UI.banner(e.message, "err"));
          if (passInput) { passInput.value = ""; passInput.focus(); }
        });
      });
    }
  }

  global.Views.addresses = { title: "Addresses", render: render };
})(window);
