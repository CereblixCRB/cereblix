# Dependency Audit — Cereblix Wallet

Goal being proven here: **the user downloads one file and runs it — no extra
libraries, runtimes, or installers to fetch.** Below is every dependency for the
Desktop and Android builds, split into *ships-to-user / runtime* vs *build-only*,
with a justification for each.

---

## 1. Frontend (Desktop **and** Android) — ZERO third-party dependencies

| Item | Source | Why it's not a dependency |
|------|--------|---------------------------|
| HTML / CSS / JS | Hand-written, in `frontend/` | No framework, no UI library. |
| Bundler / transpiler | **none** | No webpack/vite/esbuild/babel. Files are served/embedded as-is. |
| Package manager | **none** | No `package.json`, no `node_modules`, no lockfile. Node.js is **not** required to develop or build. |
| Web fonts / icons | system fonts + inline SVG | No Google Fonts, no FontAwesome, no CDN. |
| **QR code generator** | a small **MIT-licensed pure-JS** implementation **inlined** into a source file | Self-contained, **makes no network calls**. Receive-address QR works fully offline. |
| Network calls to 3rd parties | **none** | The UI only talks to the Go backend via Wails bindings (desktop) / the Kotlin bridge (Android). No analytics, no telemetry, no remote scripts. |

This is the core of the promise: there is **nothing to install** for the frontend
and **no remote asset** is ever loaded.

---

## 2. Desktop — Go backend

The desktop module (`go.mod` = `cereblix-desktop`) adds exactly **one** new direct
dependency on top of the coin module:

| Module | Role | Justification |
|--------|------|---------------|
| `github.com/wailsapp/wails/v2` | **The GUI framework.** Bridges the Go backend to the OS-native webview and auto-marshals method calls. | The single allowed extra dependency — it *is* the desktop GUI runtime. |
| `cereblix` *(via `replace cereblix => ../`)* | The coin module: `cereblix/core` (ed25519 keys, signing, tx/block types, `CoinUnit`) and `cereblix/node` (optional embedded full node). | Reuses audited consensus/crypto code instead of reimplementing it. Pulled by `replace`, so the coin stays a **separate, zero-extra-deps** module. |
| Go standard library | crypto (`ed25519`, `aes`, `sha256`, `pbkdf2` via the wallet's own impl), `net/http`, `encoding/json`, `embed`, … | No third-party crypto or HTTP libs. |

### Transitive deps inherited (not added by us)

- **From the coin module (pre-existing):** `github.com/coder/websocket`,
  `go.etcd.io/bbolt`, and the indirect `golang.org/x/sys`. These already ship in
  the coin and are the only ones the embedded full node needs. **We add none.**
- **From Wails:** Wails brings its own transitive tree (e.g. its webview2 Go
  loader, `leaanthony/*` helpers, etc.). These are vendored *by the framework*,
  not introduced by our code, and are the unavoidable internals of "the GUI
  framework" — the one dependency we accepted. Our module's **direct** requires
  remain just `wails/v2` (+ the `replace`d coin).

### What the END USER must have at runtime (per OS)

| OS | Runtime dependency | Pre-installed? |
|----|--------------------|----------------|
| **Windows 11** | **WebView2** runtime | **Yes — ships with Windows 11** (and present on Win10 since the 2021 Evergreen rollout). The `.exe` embeds the frontend; nothing else to install. |
| **Linux** | `webkit2gtk` + `gtk3` shared libs | Present on virtually all desktop distros; otherwise one `apt/dnf` package. (Listed in `build-all.sh`.) |
| **macOS** | `WKWebView` (system framework) | **Yes — part of macOS.** The `.app` is self-contained. |

The binary itself is statically self-contained (frontend embedded via
`//go:embed`); the only external piece on each OS is that OS's **own** webview,
which the platform already provides.

### Build-only tools (never shipped to the user)

| Tool | Why |
|------|-----|
| Go 1.25 (`toolchain/go`) | Compiles the backend. Pinned locally; `GOTOOLCHAIN=local`. |
| Wails CLI v2.12 (`%USERPROFILE%\go\bin\wails.exe`) | Drives `wails build` / `wails dev`. |
| GCC (ucrt64, `C:\msys64\ucrt64\bin\gcc.exe`) | CGO compiler for native webview/bbolt bits. |

None of these are in the shipped artifact.

---

## 3. Android — dependency audit

Android reuses the **same Go core** and the **same zero-dep frontend**; the only
additions are the standard Android platform pieces.

### Ships in the APK (runtime)

| Item | Role | Justification |
|------|------|---------------|
| `cereblix.aar` (gomobile-built) | The shared Go crypto/signing/RPC core, compiled from `cereblix/core` (stdlib only). | Same audited code as desktop — keys/signing stay native and local. No extra Go deps. |
| Kotlin stdlib + minimal AndroidX (`appcompat`, `webkit`) | The thin app shell hosting a `WebView`. | Standard Android platform libraries; small and bundled into the APK. No third-party UI framework. |
| Frontend assets | The identical vanilla HTML/CSS/JS in `assets/`. | Zero JS dependencies (see §1). |

### Provided by the device (runtime)

| Item | Pre-installed? |
|------|----------------|
| **System Android WebView** (renders the UI) | **Yes** — part of Android; **no browser engine is bundled** in the APK. |

### Build-only (never shipped)

| Tool | Why |
|------|-----|
| Android SDK + Build-Tools (`C:\android-build`) | `aapt`/`d8`/packaging into an APK. |
| Android NDK | Native ABI compilation for the gomobile AAR. |
| `gomobile` / `gobind` | Generates `cereblix.aar` from the Go core. |
| Gradle + Kotlin compiler | Builds and signs the APK. |

**Result:** a **single APK**. Keys never leave the phone (same encrypted
`wallet.json` scheme), the UI runs in the device's own WebView, and the frontend
carries no JavaScript dependencies.

---

## 4. Summary

- **Frontend:** 0 third-party dependencies, 0 network assets, on every platform.
- **Desktop Go:** +1 direct dependency (`wails/v2`); everything else is stdlib or
  the coin's *existing* deps. No new `go.mod require` beyond the framework.
- **End user:** downloads one self-contained file (`.exe` / `.app` / Linux binary
  / `.apk`). The only external runtime is the OS's **own** webview, which Windows
  11, macOS, and Android already include, and which Linux desktops ship by default.

The "download-and-use, no extra libraries for the user" goal holds for all
targets.
