# Cereblix Wallet (Desktop)

A native desktop GUI wallet for **Cereblix (CRB)** — a CPU-only, ed25519,
NeuroMorph-PoW cryptocurrency. It is a thin, safe front-end over the exact same
crypto, key store, and node RPC that the official `cereblix-wallet` CLI uses, so
**wallets are 100% compatible** in both directions.

Built with **Wails v2** (a Go backend + a system-webview UI). The Go side reuses
the coin's own packages (`cereblix/core` for keys/signing/types, `cereblix/node`
for the optional embedded full node); the UI is **pure vanilla HTML/CSS/JS**.

---

## The minimal-dependency promise

This is a product for end users, so the dependency surface is kept deliberately tiny:

- **Frontend: zero dependencies.** No npm, no `package.json`, no `node_modules`,
  no bundler (webpack/vite/esbuild), no framework (React/Vue/Svelte), no CDN, no
  web fonts. Just hand-written `.html`, `.css`, and `.js`. The QR code generator
  is a small self-contained MIT pure-JS implementation inlined into a source file
  — it makes **no network calls**.
- **Go backend: one new dependency** — `github.com/wailsapp/wails/v2` (the GUI
  framework). Everything else is the **Go standard library** plus the coin
  module's *existing* deps (`github.com/coder/websocket`, `go.etcd.io/bbolt`).
  No other `go.mod` `require` is added.
- The built binary is **self-contained** (the frontend is embedded with
  `//go:embed`). On Windows 11 the WebView2 runtime is already part of the OS, so
  the user just downloads and runs one `.exe`. Full audit in
  [`DEPENDENCIES.md`](DEPENDENCIES.md).

---

## Project layout

```
desktop/                     # this module (go.mod module "cereblix-desktop")
├─ go.mod                    # require wails/v2 ; replace cereblix => ../
├─ wails.json                # vanilla frontend, no build step
├─ main.go                   # wails app bootstrap, //go:embed all:frontend
├─ app.go (+ peers)          # the App struct: bindings the UI calls
├─ frontend/                 # pure HTML/CSS/JS (embedded, no npm)
├─ build-windows.ps1         # Windows build (pinned local Go + ucrt64 gcc)
└─ build-all.sh              # Linux/macOS build notes
```

The coin module sits one level up (`../`) and is pulled in via a `replace`
directive so it stays a separate, zero-extra-deps module.

---

## Running in development

```powershell
# from the desktop module root
$env:PATH = "C:\Users\Lisa\Desktop\Cereblix\toolchain\go\bin;C:\msys64\ucrt64\bin;$env:PATH"
$env:CC = "C:\msys64\ucrt64\bin\gcc.exe"
wails dev
```

`wails dev` serves the embedded frontend with hot-reload and opens the app with
the webview devtools available. Because the frontend has no build pipeline, edits
to files under `frontend/` are picked up directly — there is no dev server to
start and nothing to `npm install`.

---

## Building

### Windows (recommended path)

```powershell
powershell -ExecutionPolicy Bypass -File build-windows.ps1
```

This pins the local Go (`toolchain\go\bin\go.exe`), the Wails CLI
(`%USERPROFILE%\go\bin\wails.exe`), and the ucrt64 GCC
(`C:\msys64\ucrt64\bin\gcc.exe`), then runs `wails build -clean`.
Output: `build\bin\cereblix-wallet.exe` — a single distributable file.

### Linux / macOS

See [`build-all.sh`](build-all.sh). Each OS must be built **on that OS** because
Wails links the platform's native webview (WebView2 / WebKit2GTK / WKWebView),
which cannot be cross-compiled. macOS in particular must be built on a Mac.

---

## Node connectivity — three modes

The wallet never needs its own chain to spend or receive; it talks to a node's
HTTP RPC (`/balance`, `/status`, `/history`, `POST /tx`, …). Choose a mode in
**Settings** (persisted in `~/.cereblix/`):

| Mode | What it does | Endpoint |
|------|--------------|----------|
| **Lite** *(default)* | Talks to the public remote node over HTTPS. Fast start, no sync, no disk. | `https://cereblix.com/api` |
| **Full** *(optional)* | Spins up an **embedded in-process node** (`cereblix/node` + `core.OpenChain` on bbolt) and points the wallet at its local RPC. Validates independently; uses disk + bandwidth to sync. | `http://127.0.0.1:18751` |
| **Custom** | Any node RPC base URL you supply (your own VPS, a LAN node, etc.). | user-provided |

`NodeInfo()` reports the active mode, whether the node is reachable, and sync
height; Full mode is started/stopped with `StartFullNode()` / `StopFullNode()`.

---

## Security model

- **Keys are generated, stored, and used 100% locally.** ed25519 private keys
  never leave the machine and are **never sent to the frontend** — the only
  binding that returns a private key is the explicit `ExportKey(addrOrLabel,
  passphrase)` call the user deliberately triggers.
- **Encrypted at rest, same scheme as the CLI.** The wallet file is
  `~/.cereblix/wallet.json` in the identical format the CLI writes: PBKDF2-HMAC-
  SHA256 with **200,000** iterations derives a 32-byte key that encrypts the key
  array with **AES-256-GCM**. Existing CLI wallets open unchanged, and wallets
  created here open in the CLI.
- **Signing is local.** Transactions are built and signed in the Go backend
  (`core.SignTxAt`) and only the signed `core.Tx` is POSTed to the node. The node
  (remote, embedded, or custom) never sees a private key.
- **Auto-lock.** The wallet locks after an idle timeout (configurable); a locked
  wallet holds no decrypted keys in memory and requires the passphrase to unlock.
- Amounts are integer "synapses" on the wire (`1 CRB = 100,000,000 synapses`) and
  only formatted to an 8-decimal CRB string for display — no floating-point math
  touches consensus values.

---

## Android version

Wails targets desktop only, so Android is a **separate, parallel build that
reuses the same Go core and the same vanilla frontend** — no logic is rewritten:

1. **Shared core as an AAR.** The pure-Go crypto/signing/RPC layer (built on
   `cereblix/core`, stdlib only) is compiled to an Android library with
   **gomobile**:
   ```sh
   go install golang.org/x/mobile/cmd/gomobile@latest
   gomobile init
   gomobile bind -target=android -androidapi 21 -o app/libs/cereblix.aar ./mobilebind
   ```
   This produces `cereblix.aar` exposing the same wallet operations (create/
   unlock/list/send/history/…) to the Android app.
2. **Thin Kotlin shell.** A standard Android Studio / Gradle project (Kotlin +
   AndroidX) hosts a **system `WebView`** that loads the *same* `frontend/`
   assets from `app/src/main/assets/`. The JS calls a small Kotlin↔JS bridge
   that forwards to the AAR — mirroring the desktop Wails bindings — so the UI
   code is identical.
3. **Build the APK.** With the Android SDK + NDK installed (the same toolchain at
   `C:\android-build` used for the miner APK):
   ```sh
   ./gradlew assembleRelease   # -> app/build/outputs/apk/release/*.apk
   ```

The result is a **single APK**: keys stay on the phone (same encrypted store),
the UI uses the device's system WebView (no bundled browser engine), and the
frontend still ships **zero JS dependencies**. See [`DEPENDENCIES.md`](DEPENDENCIES.md)
for the Android dependency audit.

---

## License

Same license as the Cereblix coin module (see the repository root `LICENSE`).
