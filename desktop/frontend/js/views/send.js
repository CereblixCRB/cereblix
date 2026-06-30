/* ============================================================================
   views/send.js — compose, review, confirm and broadcast a transaction.
   ========================================================================== */
(function (global) {
  "use strict";
  var UI = global.UI, API = global.API, Store = global.Store, Router = global.Router, el = UI.el;
  global.Views = global.Views || {};

  function render(mount, params, ctx) {
    var suggested = "0.001";
    API.SuggestedFee().then(function (f) { suggested = f || suggested; var fi = UI.$("#sd-fee"); if (fi && !fi.dataset.touched) fi.placeholder = "Suggested: " + UI.prettyCrb(suggested); }).catch(function () {});

    showForm({ to: params.to || "", from: params.from || "", amount: params.amount || "" });

    function fromOptions(selected) {
      var opts = [el("option", { value: "", text: "Auto — pick a funded address", selected: !selected })];
      (Store.state.addresses || []).forEach(function (a) {
        opts.push(el("option", { value: a.Addr, selected: selected === a.Addr,
          text: (a.Label || "Address") + " · " + UI.prettyCrb(a.Balance) + " CRB · " + UI.shortMid(a.Addr, 8, 6) }));
      });
      return opts;
    }

    // -------------------------------- form --------------------------------
    function showForm(pre) {
      UI.clear(mount);
      var err = el("div");
      var fromSel = el("select", { class: "select", id: "sd-from" }, fromOptions(pre.from));

      var toInput = el("input", { class: "input mono", id: "sd-to", placeholder: "crb1…", value: pre.to || "", autocomplete: "off", spellcheck: "false" });
      var toMsg = el("div", { class: "field-msg" });
      var validTo = false;
      var checkTo = UI.debounce(function () {
        var v = toInput.value.trim();
        if (!v) { toInput.classList.remove("valid", "invalid"); UI.clear(toMsg); validTo = false; return; }
        API.ValidateAddress(v).then(function (ok) {
          validTo = ok;
          toInput.classList.toggle("valid", ok); toInput.classList.toggle("invalid", !ok);
          UI.clear(toMsg);
          toMsg.className = "field-msg " + (ok ? "ok" : "err");
          toMsg.innerHTML = (ok ? UI.icon("check", 13) + " Valid Cereblix address" : UI.icon("warning", 13) + " Not a valid CRB address");
          if (ok && Store.isOurs(v)) { toMsg.className = "field-msg"; toMsg.innerHTML = UI.icon("info", 13) + " This is one of your own addresses"; }
        }).catch(function () {});
      }, 220);
      toInput.addEventListener("input", checkTo);
      if (pre.to) checkTo();

      var amtInput = el("input", { class: "input mono", id: "sd-amount", inputmode: "decimal", placeholder: "0.00000000", value: pre.amount || "" });
      var maxBtn = el("button", { type: "button", class: "link-btn", text: "Max" });
      maxBtn.addEventListener("click", function () {
        var a = (Store.state.addresses || []).filter(function (x) { return x.Addr === fromSel.value; })[0];
        if (!a) { UI.toast("Choose a specific from-address to use Max", "info"); return; }
        var bal = parseFloat(String(a.Balance).replace(/,/g, "")) || 0;
        var fee = parseFloat(feeInput.value || suggested) || 0;
        var max = Math.max(0, bal - fee);
        amtInput.value = max.toFixed(8).replace(/0+$/, "").replace(/\.$/, "");
      });

      var feeInput = el("input", { class: "input mono", id: "sd-fee", inputmode: "decimal", placeholder: "Suggested: " + UI.prettyCrb(suggested) });
      feeInput.addEventListener("input", function () { feeInput.dataset.touched = "1"; });
      var feeWrap = el("div", { class: "disclosure", style: "display:none" }, [
        el("div", { class: "field", style: "margin:0" }, [
          el("label", { for: "sd-fee", text: "Network fee (CRB)" }),
          el("div", { class: "input-affix" }, [feeInput, el("span", { class: "suffix", text: "CRB" })]),
          el("div", { class: "hint" }, [
            "Higher fees confirm sooner. ",
            el("button", { type: "button", class: "link-btn", text: "Use suggested", onclick: function () { feeInput.value = ""; delete feeInput.dataset.touched; } })
          ])
        ])
      ]);
      var advToggle = el("button", { type: "button", class: "disclosure-toggle", html: UI.icon("settings", 15) + " Advanced — set custom fee" });
      advToggle.addEventListener("click", function () {
        var open = feeWrap.style.display !== "none";
        feeWrap.style.display = open ? "none" : "block";
      });

      var submitBtn = el("button", { class: "btn btn-primary btn-lg", type: "submit", text: "Review" });
      var form = el("form", { onsubmit: function (e) { e.preventDefault(); review(); } }, [
        err,
        el("div", { class: "field" }, [el("label", { for: "sd-from", text: "From" }), fromSel]),
        el("div", { class: "field" }, [el("label", { for: "sd-to", text: "Recipient address" }), toInput, toMsg]),
        el("div", { class: "field" }, [
          el("label", { class: "row-between", for: "sd-amount" }, [el("span", { text: "Amount" }), maxBtn]),
          el("div", { class: "input-affix" }, [amtInput, el("span", { class: "suffix", text: "CRB" })])
        ]),
        el("div", { class: "field", style: "margin-bottom:8px" }, [advToggle, feeWrap]),
        el("div", { style: "display:flex;gap:12px;margin-top:8px" }, [submitBtn])
      ]);
      submitBtn.style.flex = "1";
      mount.appendChild(el("div", { class: "card card-pad", style: "max-width:560px;margin:0 auto" }, [form]));

      function review() {
        UI.clear(err);
        var to = toInput.value.trim();
        var amount = (amtInput.value || "").trim();
        if (!validTo) { err.appendChild(UI.banner("Enter a valid recipient address.", "err")); toInput.focus(); return; }
        var amtNum = parseFloat(amount);
        if (!(amtNum > 0)) { err.appendChild(UI.banner("Enter an amount greater than zero.", "err")); amtInput.focus(); return; }
        var feeOverride = feeInput.dataset.touched ? (feeInput.value || "").trim() : "";
        if (feeOverride && !(parseFloat(feeOverride) >= 0)) { err.appendChild(UI.banner("Fee must be a non-negative number.", "err")); return; }
        showReview({ from: fromSel.value, fromLabel: fromSel.options[fromSel.selectedIndex].text, to: to, amount: amount, fee: feeOverride });
      }
    }

    // ------------------------------- review -------------------------------
    function showReview(d) {
      UI.clear(mount);
      var err = el("div");
      var feeDisplay = d.fee ? UI.prettyCrb(d.fee) : UI.prettyCrb(suggested) + "  (suggested)";
      var total = (parseFloat(d.amount) + parseFloat(d.fee || suggested)).toFixed(8).replace(/0+$/, "").replace(/\.$/, "");
      var confirmBtn = el("button", { class: "btn btn-primary btn-lg", text: "Confirm & send" });
      confirmBtn.style.flex = "1";

      var card = el("div", { class: "card card-pad", style: "max-width:560px;margin:0 auto" }, [
        el("h2", { class: "card-title", text: "Review transaction" }),
        err,
        el("div", { class: "review" }, [
          rline("From", d.from ? UI.shortMid(d.from, 10, 8) : "Auto-selected"),
          rline("To", UI.shortMid(d.to, 12, 10)),
          rline("Amount", d.amount + " CRB"),
          rline("Network fee", feeDisplay + " CRB"),
          el("div", { class: "r-line total" }, [el("span", { class: "k", text: "Total" }), el("span", { class: "v", text: total + " CRB" })])
        ]),
        UI.banner("Transactions are irreversible. Double-check the recipient address.", "warn"),
        el("div", { style: "display:flex;gap:12px;margin-top:4px" }, [
          el("button", { class: "btn btn-ghost", text: "Back", onclick: function () { showForm({ from: d.from, to: d.to, amount: d.amount }); } }),
          confirmBtn
        ])
      ]);
      mount.appendChild(card);
      setTimeout(function () { confirmBtn.focus(); }, 30);

      confirmBtn.addEventListener("click", function () {
        UI.clear(err);
        confirmBtn.disabled = true; confirmBtn.innerHTML = '<span class="spinner"></span> Broadcasting…';
        API.Send(d.from, d.to, d.amount, d.fee).then(function (res) {
          Store.refreshAddresses().catch(function () {});
          showSuccess(res && res.Txid, d);
        }).catch(function (e) {
          confirmBtn.disabled = false; confirmBtn.textContent = "Confirm & send";
          err.appendChild(UI.banner(e.message, "err"));
        });
      });
    }

    // ------------------------------ success -------------------------------
    function showSuccess(txid, d) {
      UI.clear(mount);
      var card = el("div", { class: "card card-pad", style: "max-width:560px;margin:0 auto;text-align:center" }, [
        el("div", { class: "success-mark", html: UI.icon("check", 30) }),
        el("h2", { style: "margin:0 0 6px", text: "Transaction sent" }),
        el("p", { class: "dim", text: d.amount + " CRB is on its way." }),
        el("div", { class: "field", style: "text-align:left;margin-top:20px" }, [
          el("label", { text: "Transaction ID" }),
          el("div", { class: "review" }, [el("div", { class: "r-line" }, [UI.copyable(txid || "", { label: "Transaction ID", display: UI.shortMid(txid || "", 14, 12) })])])
        ]),
        el("div", { class: "btn-row", style: "justify-content:center;margin-top:8px" }, [
          el("button", { class: "btn btn-sm", html: UI.icon("refresh", 15) + "<span>Speed up</span>", onclick: function () { bump("SpeedUp", txid); } }),
          el("button", { class: "btn btn-sm btn-danger", html: UI.icon("x", 15) + "<span>Cancel tx</span>", onclick: function () { bump("Cancel", txid); } }),
          el("button", { class: "btn btn-sm btn-ghost", html: UI.icon("explorer", 15) + "<span>View</span>", onclick: function () { Router.go("explorer", { q: txid }); } })
        ]),
        el("div", { style: "margin-top:24px;display:flex;gap:12px;justify-content:center" }, [
          el("button", { class: "btn btn-ghost", text: "Done", onclick: function () { Router.go("dashboard"); } }),
          el("button", { class: "btn btn-primary", text: "Send another", onclick: function () { showForm({}); } })
        ])
      ]);
      mount.appendChild(card);
    }

    function bump(method, txid) {
      UI.confirm({
        title: method === "Cancel" ? "Cancel transaction?" : "Speed up transaction?",
        message: method === "Cancel"
          ? "Broadcast a replacement that returns the funds to you (replace-by-fee). Only works while the original is still pending."
          : "Rebroadcast with a higher fee so it confirms sooner (replace-by-fee).",
        okText: method === "Cancel" ? "Cancel tx" : "Speed up",
        danger: method === "Cancel"
      }).then(function (yes) {
        if (!yes) return;
        API[method](txid, "").then(function (res) {
          UI.toast((method === "Cancel" ? "Cancellation" : "Replacement") + " broadcast", "ok");
          Store.refreshAddresses().catch(function () {});
          if (res && res.Txid) Router.go("explorer", { q: res.Txid });
        }).catch(function (e) { UI.toast(e.message, "err", 4500); });
      });
    }

    function rline(k, v) { return el("div", { class: "r-line" }, [el("span", { class: "k", text: k }), el("span", { class: "v", text: v })]); }
  }

  global.Views.send = { title: "Send", render: render };
})(window);
