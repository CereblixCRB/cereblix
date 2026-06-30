# 0009. GUI wallet architecture (Wails desktop + gomobile Android)

## Status

Accepted — 2026-06-30

## Context

Cereblix shipped two wallets: a browser **web wallet** and a **CLI wallet**
(`cmd/cereblix-wallet`). Neither is a native GUI app, which most end users expect.
We wanted a desktop GUI and an Android GUI that are:

1. **Crypto-identical** to the existing wallets — same ed25519 keys, same signing,
   same on-disk format — so a wallet works everywhere with no migration; and
2. built **without rewriting consensus/crypto** (re-implementing signing invites
   subtle, fund-losing bugs) and **without a heavy dependency tree** (the promise
   is "download one file and run it").

## Decision

We will build both GUIs as **thin front-ends over the coin's own `cereblix/core`**
package — the single source of keys, signing and types — so all three wallets
(CLI, desktop, Android) are byte-for-byte crypto-compatible:

- **Desktop:** **Wails v2** (a Go backend + the OS-native system webview). The
  frontend is **pure vanilla HTML/CSS/JS with zero JavaScript dependencies** (no
  npm, no framework, no bundler, no CDN; even the QR generator is an inlined
  pure-JS implementation). The Go side adds exactly **one** new direct dependency,
  `wails/v2`; everything else is the standard library plus the coin module's
  existing deps. The frontend is embedded with `//go:embed`, so the artifact is a
  single self-contained binary.
- **Android:** a **gomobile**-built AAR compiled from `cereblix/core` (the only
  place keys are generated and signed), linked by a thin Kotlin app. Keys stay on
  the phone in the same encrypted store. No coin-module dependency is added by the
  binding.
- **Key custody & node connectivity:** keys are generated, stored and used 100%
  locally and are never sent to the frontend; signing happens in the Go backend
  and only the signed transaction is POSTed. The wallet file uses the **same scheme
  as the CLI** (`~/.cereblix/wallet.json`, PBKDF2-HMAC-SHA256 → AES-256-GCM) and an
  idle auto-lock. The default node mode is **Lite** — remote node RPC over HTTPS
  with round-robin failover — with an optional **Full** mode (an in-process
  embedded `cereblix/node` on bbolt, reached on loopback) and a **Custom** URL mode.

## Consequences

- One audited crypto path across CLI, desktop and Android; a wallet created in any
  one opens in the others unchanged.
- Tiny dependency surface and a single self-contained artifact per platform; the
  only external runtime piece is the OS's *own* webview (already present on
  Windows 11 / macOS / Android; one package on Linux).
- Cost: native webviews **cannot be cross-compiled**, so each desktop OS must be
  built on that OS (macOS on a Mac). Wails brings its own transitive tree (the
  accepted price of "the GUI framework"). The embedded Full node has no graceful
  shutdown, so "stop Full" simply routes back to Lite while the node lingers for
  the process lifetime.
- Both GUIs live in the **public** repo (`desktop/`, `wallet-android/`), consistent
  with ADR 0001 — they are part of the open-source coin, not operator infrastructure.
