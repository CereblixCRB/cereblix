/* ============================================================================
   router.js — minimal hash router. No framework.
   Parses "#/name?k=v&k2=v2" into {name, params}. The actual rendering +
   route-guarding lives in app.js (it has Store access); the router only
   parses, dispatches and exposes navigation helpers. Exposed as window.Router.
   ========================================================================== */
(function (global) {
  "use strict";

  var APP_ROUTES = ["dashboard", "send", "receive", "history", "addresses", "explorer", "settings"];
  var handler = function () {};

  function parse() {
    var h = location.hash.replace(/^#\/?/, "");
    var qi = h.indexOf("?");
    var name = (qi === -1 ? h : h.slice(0, qi)) || "dashboard";
    var params = {};
    if (qi !== -1) {
      h.slice(qi + 1).split("&").forEach(function (kv) {
        if (!kv) return;
        var p = kv.split("=");
        params[decodeURIComponent(p[0])] = decodeURIComponent((p[1] || "").replace(/\+/g, " "));
      });
    }
    return { name: name, params: params };
  }

  function go(name, params) {
    var hash = "#/" + name;
    if (params) {
      var q = Object.keys(params).filter(function (k) { return params[k] != null && params[k] !== ""; })
        .map(function (k) { return encodeURIComponent(k) + "=" + encodeURIComponent(params[k]); }).join("&");
      if (q) hash += "?" + q;
    }
    if (location.hash === hash) dispatch(); // re-run even if unchanged
    else location.hash = hash;
  }

  function replace(name, params) {
    var route = parse();
    go(name, params);
  }

  function dispatch() { handler(parse()); }

  function start(fn) {
    handler = fn || handler;
    global.addEventListener("hashchange", dispatch);
    dispatch();
  }

  global.Router = { start: start, go: go, replace: replace, parse: parse, dispatch: dispatch, APP_ROUTES: APP_ROUTES };
})(window);
