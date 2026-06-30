# Git hooks — secret-scanning backstop

This directory holds a `pre-commit` hook that runs **[gitleaks](https://github.com/gitleaks/gitleaks)**
against your **staged** changes before each commit, so an ed25519 key, a `ghp_`
token, or any other credential can't slip into a commit. The project has had a
real secret-leak incident, so please enable it.

## Enable (once per clone)

```sh
git config core.hooksPath .githooks
```

This points git at this directory for hooks (git does not run repo-tracked hooks
automatically). Verify:

```sh
git config --get core.hooksPath      # -> .githooks
```

On Unix, also make sure the hook is executable:

```sh
chmod +x .githooks/pre-commit
```

## Install gitleaks (the hook needs it)

| Platform | Install |
|----------|---------|
| macOS / Linux | `brew install gitleaks` |
| Go | `go install github.com/gitleaks/gitleaks/v8@latest` |
| Windows | `scoop install gitleaks` (or a [release binary](https://github.com/gitleaks/gitleaks/releases)) |

If gitleaks is **not** installed, the hook prints a warning and lets the commit
through — it never blocks a teammate who hasn't installed the tool yet. The
authoritative enforcement is the CI gate in
[`.github/workflows/gitleaks.yml`](../.github/workflows/gitleaks.yml), which runs
on every push and pull request and **cannot** be bypassed with `--no-verify`.

## What it does

- Scans only the **staged** diff: `gitleaks protect --staged --redact`.
- Uses the ruleset + allowlist in [`../.gitleaks.toml`](../.gitleaks.toml).
- Secrets live ONLY in `.secrets/` (gitignored); that path is allowlisted so it
  never trips the scanner. Compiled artifacts (`*.aar`, `*.wasm`, `*.exe`) are
  allowlisted too.

## Bypass (sparingly — only for a verified false positive)

```sh
git commit --no-verify
```

Prefer adding a narrow allowlist entry to `.gitleaks.toml` over routinely using
`--no-verify`. Remember: CI will still scan the push/PR regardless.
