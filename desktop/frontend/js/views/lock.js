/* ============================================================================
   views/lock.js — unlock an existing encrypted wallet by passphrase.
   Full-screen. Calls window.App.enterApp() on success.
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, API = global.API, Store = global.Store, el = UI.el;
  global.Views = global.Views || {};

  function render(root) {
    UI.clear(root);
    var err = el("div");
    var input = el("input", { class: "input", type: "password", id: "lk-pass", placeholder: "Passphrase", autocomplete: "current-password", autofocus: true });
    var toggle = el("button", { class: "btn btn-ghost btn-icon", type: "button", "aria-label": "Show passphrase", html: UI.icon("eye", 18),
      onclick: function () { var s = input.type === "password"; input.type = s ? "text" : "password"; toggle.innerHTML = UI.icon(s ? "eyeOff" : "eye", 18); input.focus(); } });
    var btn = el("button", { class: "btn btn-primary btn-lg btn-block", text: "Unlock" });

    var form = el("form", { onsubmit: function (e) { e.preventDefault(); submit(); } }, [
      el("div", { class: "auth-brand" }, [
        el("div", { class: "logo", html: '<span style="display:grid;place-items:center;height:100%;color:var(--text-on-accent)">' + UI.icon("lock", 26) + "</span>" }),
        el("h1", { text: "Wallet locked" }),
        el("p", { text: "Enter your passphrase to continue." })
      ]),
      err,
      el("div", { class: "field" }, [
        el("label", { for: "lk-pass", text: "Passphrase" }),
        el("div", { class: "input-with-btn" }, [input, toggle])
      ]),
      btn
    ]);

    var card = el("div", { class: "auth-card" }, [form]);
    root.appendChild(el("div", { class: "fullscreen" }, [card]));
    setTimeout(function () { input.focus(); }, 40);

    function submit() {
      UI.clear(err);
      if (!input.value) { input.focus(); return; }
      btn.disabled = true; btn.innerHTML = '<span class="spinner"></span> Unlocking…';
      API.Unlock(input.value).then(function (ok) {
        if (ok) { Store.markActivity(); return Store.refreshWallet().then(Store.refreshAddresses).then(function () { global.App.enterApp(); }); }
        reset("Incorrect passphrase. Try again.");
      }).catch(function (e) { reset(e.message); });
    }
    function reset(msg) {
      btn.disabled = false; btn.textContent = "Unlock";
      err.appendChild(UI.banner(msg, "err"));
      input.value = ""; input.focus();
    }
  }

  global.Views.lock = { render: render, fullscreen: true };
})(window);
