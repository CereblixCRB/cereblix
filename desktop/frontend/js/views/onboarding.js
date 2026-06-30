/* ============================================================================
   views/onboarding.js — first-run: create a new wallet OR import a key.
   Full-screen. Calls window.App.enterApp() on success.
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, API = global.API, Store = global.Store, el = UI.el;
  global.Views = global.Views || {};

  function strength(pw) {
    if (!pw) return { score: 0, label: "", color: "var(--surface-3)" };
    var s = 0;
    if (pw.length >= 8) s++; if (pw.length >= 12) s++;
    if (/[a-z]/.test(pw) && /[A-Z]/.test(pw)) s++;
    if (/\d/.test(pw)) s++; if (/[^A-Za-z0-9]/.test(pw)) s++;
    var map = [
      { label: "Very weak", color: "var(--danger)", w: 18 },
      { label: "Weak", color: "var(--danger)", w: 34 },
      { label: "Fair", color: "var(--warn)", w: 55 },
      { label: "Good", color: "var(--info)", w: 76 },
      { label: "Strong", color: "var(--pos)", w: 100 }
    ];
    return map[Math.min(s, 5) - 1] || map[0];
  }

  function passField(id, placeholder) {
    var input = el("input", { class: "input", type: "password", id: id, placeholder: placeholder || "Passphrase", autocomplete: "new-password" });
    var toggle = el("button", { class: "btn btn-ghost btn-icon", type: "button", "aria-label": "Show passphrase", html: UI.icon("eye", 18),
      onclick: function () {
        var show = input.type === "password";
        input.type = show ? "text" : "password";
        toggle.innerHTML = UI.icon(show ? "eyeOff" : "eye", 18);
      } });
    var wrap = el("div", { class: "input-with-btn" }, [input, toggle]);
    return { wrap: wrap, input: input };
  }

  function header(title, sub) {
    return el("div", { class: "auth-brand" }, [
      el("div", { class: "logo" }),
      el("h1", { text: title }),
      el("p", { text: sub })
    ]);
  }

  function render(root) {
    UI.clear(root);
    var stage = el("div", { class: "fullscreen" });
    var card = el("div", { class: "auth-card" });
    stage.appendChild(card); root.appendChild(stage);
    showChoice();

    function showChoice() {
      UI.clear(card);
      UI.append(card, [
        header("Welcome to Cereblix", "Your self-custodial CRB wallet. Keys never leave this device."),
        el("div", { class: "onb-choices" }, [
          choice("sparkle", "Create a new wallet", "Generate a fresh address and start receiving CRB.", showCreate),
          choice("import", "Import a private key", "Restore access from an existing 128-hex ed25519 key.", showImport)
        ]),
        el("p", { class: "faint center-text", style: "margin-top:24px;font-size:12px", text: "All signing happens locally. Back up your keys — they cannot be recovered." })
      ]);
    }
    function choice(ic, title, sub, onclick) {
      var b = el("button", { class: "choice", type: "button", onclick: onclick }, [
        el("span", { class: "c-ic", html: UI.icon(ic, 22) }),
        el("span", { class: "c-body" }, [el("h3", { text: title }), el("p", { text: sub })]),
        el("span", { class: "c-arrow", html: UI.icon("chevronR", 20) })
      ]);
      return b;
    }

    // ---------------- create ----------------
    function showCreate() {
      UI.clear(card);
      var err = el("div");
      var pf = passField("nw-pass", "Passphrase (min 8 characters)");
      var cf = passField("nw-pass2", "Confirm passphrase");
      var meter = el("i"); var meterLabel = el("span", { class: "hint" });
      pf.input.addEventListener("input", function () {
        var st = strength(pf.input.value);
        meter.style.width = st.w + "%"; meter.style.background = st.color;
        meterLabel.textContent = pf.input.value ? "Passphrase strength: " + st.label : "Required — encrypts wallet.json with AES-GCM";
      });
      meterLabel.textContent = "Required — encrypts wallet.json with AES-GCM";

      var btn = el("button", { class: "btn btn-primary btn-lg btn-block", text: "Create wallet" });
      var form = el("form", { onsubmit: function (e) { e.preventDefault(); submit(); } }, [
        header("Create a new wallet", "Set a passphrase to encrypt your keys at rest."),
        err,
        el("div", { class: "field" }, [el("label", { for: "nw-pass", text: "Passphrase" }), pf.wrap,
          el("div", { class: "meter" }, [meter]), meterLabel]),
        el("div", { class: "field" }, [el("label", { for: "nw-pass2", text: "Confirm passphrase" }), cf.wrap]),
        el("div", { class: "btn-row", style: "margin-top:8px" }, [
          el("button", { class: "btn btn-ghost", type: "button", text: "Back", onclick: showChoice }),
          btn
        ])
      ]);
      btn.type = "submit"; btn.style.flex = "1";
      card.appendChild(form);
      setTimeout(function () { pf.input.focus(); }, 30);

      function submit() {
        UI.clear(err);
        var p1 = pf.input.value, p2 = cf.input.value;
        if (!p1 || p1.length < 8) { err.appendChild(UI.banner("Choose a passphrase of at least 8 characters.", "err")); pf.input.focus(); return; }
        if (p1 !== p2) { err.appendChild(UI.banner("Passphrases do not match.", "err")); return; }
        setBusy(btn, true, "Creating…");
        API.CreateWallet(p1).then(function (k) {
          return Store.refreshWallet().then(Store.refreshAddresses).then(function () { showCreated(k); });
        }).catch(function (e) { setBusy(btn, false, "Create wallet"); err.appendChild(UI.banner(e.message, "err")); });
      }
    }

    function showCreated(k) {
      UI.clear(card);
      UI.append(card, [
        el("div", { class: "auth-brand" }, [
          el("div", { class: "success-mark", html: UI.icon("check", 30) }),
          el("h1", { text: "Wallet ready" }),
          el("p", { text: "Your first address has been created." })
        ]),
        el("div", { class: "field" }, [
          el("label", { text: (k && k.Label) ? k.Label : "Your address" }),
          UI.copyable((k && k.Addr) || "", { label: "Address", display: (k && k.Addr) || "", class: "copyable-block" })
        ]),
        UI.banner("Back up your wallet: export the private key from Settings → Security and store it somewhere safe. Lost keys cannot be recovered.", "warn"),
        el("button", { class: "btn btn-primary btn-lg btn-block", text: "Open wallet", onclick: function () { global.App.enterApp(); } })
      ]);
    }

    // ---------------- import ----------------
    function showImport() {
      UI.clear(card);
      var err = el("div");
      var keyInput = el("textarea", { class: "input mono", id: "imp-key", rows: "3", placeholder: "128 hexadecimal characters", autocomplete: "off", spellcheck: "false" });
      var labelInput = el("input", { class: "input", id: "imp-label", placeholder: "e.g. Main", maxlength: "40" });
      var pf = passField("imp-pass", "Passphrase (min 8 characters)");
      var cf = passField("imp-pass2", "Confirm passphrase");
      var btn = el("button", { class: "btn btn-primary btn-lg", text: "Import key" });
      var form = el("form", { onsubmit: function (e) { e.preventDefault(); submit(); } }, [
        header("Import a private key", "Paste a 128-hex ed25519 private key to restore an address."),
        err,
        el("div", { class: "field" }, [el("label", { for: "imp-key", text: "Private key" }), keyInput,
          el("div", { class: "hint", text: "Stays on this device. Never share it." })]),
        el("div", { class: "field" }, [el("label", { for: "imp-label", text: "Label (optional)" }), labelInput]),
        el("div", { class: "field" }, [el("label", { for: "imp-pass", text: "Encrypt with passphrase" }), pf.wrap]),
        el("div", { class: "field" }, [el("label", { for: "imp-pass2", text: "Confirm passphrase" }), cf.wrap]),
        el("div", { class: "btn-row", style: "margin-top:8px" }, [
          el("button", { class: "btn btn-ghost", type: "button", text: "Back", onclick: showChoice }),
          btn
        ])
      ]);
      btn.type = "submit"; btn.style.flex = "1";
      card.appendChild(form);
      setTimeout(function () { keyInput.focus(); }, 30);

      function submit() {
        UI.clear(err);
        var key = keyInput.value.trim().toLowerCase();
        if (!/^[0-9a-f]{128}$/.test(key)) { err.appendChild(UI.banner("Private key must be exactly 128 hexadecimal characters.", "err")); return; }
        var pass = pf.input.value, pass2 = cf.input.value;
        if (!pass || pass.length < 8) { err.appendChild(UI.banner("Choose a passphrase of at least 8 characters to encrypt the wallet.", "err")); pf.input.focus(); return; }
        if (pass !== pass2) { err.appendChild(UI.banner("Passphrases do not match.", "err")); return; }
        setBusy(btn, true, "Importing…");
        API.ImportKey(key, labelInput.value.trim()).then(function (k) {
          return API.EncryptWallet(pass)
            .then(function () { return Store.refreshWallet().then(Store.refreshAddresses); })
            .then(function () { showCreated(k); });
        }).catch(function (e) { setBusy(btn, false, "Import key"); err.appendChild(UI.banner(e.message, "err")); });
      }
    }
  }

  function setBusy(btn, busy, text) {
    btn.disabled = busy;
    btn.innerHTML = busy ? '<span class="spinner"></span> ' + UI.escapeHtml(text) : UI.escapeHtml(text);
  }

  global.Views.onboarding = { render: render, fullscreen: true };
})(window);
