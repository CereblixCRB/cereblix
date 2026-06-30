# Cereblix Android wallet — build guide

Two artifacts: a **gomobile AAR** (`wallet.aar`) that reuses the coin's Go
crypto, and the **APK** that links it. The AAR is the ONLY place keys are
generated/signed, so the Android wallet is byte-for-byte crypto-identical to the
desktop wallet (both call `cereblix/core`).

```
staging/android/
├── go.mod                      # module cereblix-mobile (replace cereblix => ../../repos/cereblix)
├── mobile/wallet.go            # package mobile — gomobile bind surface
├── android-app/                # Gradle + Jetpack Compose app
│   └── app/libs/wallet.aar     # <- produced by `gomobile bind` (step 3)
└── BUILD.md
```

## 0. Prerequisites

- **Go 1.25** — toolchain at `C:\Users\Lisa\Desktop\Cereblix\toolchain\go`.
  `set "PATH=C:\Users\Lisa\Desktop\Cereblix\toolchain\go\bin;%PATH%"`
- **JDK 17**.
- **Android SDK + NDK** at `C:\android-build` (SDK). gomobile needs the NDK.
  `set "ANDROID_HOME=C:\android-build"`
  `set "ANDROID_NDK_HOME=C:\android-build\ndk\<version>"`
- A web proxy/network for the first `go install` and the first Gradle sync.

## 1. Install gomobile

```
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
gomobile init           # downloads/wires the NDK; needs ANDROID_HOME + ANDROID_NDK_HOME
```

Make sure `%GOPATH%\bin` (default `%USERPROFILE%\go\bin`) is on PATH so `gomobile`
is found.

## 2. Wire golang.org/x/mobile into the binding module (ONLY this module)

From `staging/android` (so `go.sum` gets the x/mobile entries):

```
cd C:\Users\Lisa\Desktop\Cereblix\staging\android
go get golang.org/x/mobile/bind@latest
go mod tidy
```

> This adds `golang.org/x/mobile` to **cereblix-mobile**'s `go.mod` only — it is
> the gomobile binding tool. The coin module (`repos/cereblix/go.mod`) is NOT
> touched and still requires only `coder/websocket`, `go.etcd.io/bbolt`,
> `golang.org/x/sys`.

## 3. Build the AAR

```
cd C:\Users\Lisa\Desktop\Cereblix\staging\android
gomobile bind -target=android -androidapi 21 -o android-app/app/libs/wallet.aar ./mobile
```

Produces `android-app/app/libs/wallet.aar` exposing class `mobile.Mobile`:

| Java method | Returns |
|---|---|
| `Mobile.newAddress()` | `String` JSON `{label,addr,priv}` |
| `Mobile.addressFromPriv(String)` | `String` (addr, `""` if invalid) |
| `Mobile.validateAddress(String)` | `boolean` |
| `Mobile.signSend(String,String,long,long,long,long)` | `String` signed `core.Tx` JSON |
| `Mobile.coinUnit()` | `long` |

## 4. Point Gradle at the SDK

Create `android-app/local.properties`:

```
sdk.dir=C:\\android-build
```

## 5. Build the APK

```
cd C:\Users\Lisa\Desktop\Cereblix\staging\android\android-app
gradlew.bat assembleDebug      # or: gradlew.bat installDebug  (deploy to a device)
```

APK: `android-app/app/build/outputs/apk/debug/app-debug.apk`.

> If you don't have the Gradle wrapper jar, run `gradle wrapper --gradle-version 8.7`
> once in `android-app/` (Gradle 8.5+ is required by AGP 8.5.2).

## 6. Rebuild loop

Edit `mobile/wallet.go` → re-run step 3 (regenerate the AAR) → step 5.
Edit Kotlin only → step 5.

---

## Dependency list (every dependency, explicit)

**Binding module `cereblix-mobile` (`staging/android/go.mod`)**
- `cereblix` — the coin module, via `replace => ../../repos/cereblix` (local, no network).
- `go.etcd.io/bbolt`, `golang.org/x/sys` — indirect, inherited from the coin module's graph.
- `golang.org/x/mobile` — build-time only, added by step 2 (the gomobile binding tool).
- Everything else is the Go standard library.

**Android app (`android-app/app/build.gradle`)**
- `files('libs/wallet.aar')` — the gomobile binding (the only non-AndroidX dep).
- `androidx.core:core-ktx:1.13.1`
- `androidx.activity:activity-compose:1.9.0`
- `androidx.compose:compose-bom:2024.06.00` (BOM) → `androidx.compose.ui:ui`,
  `androidx.compose.ui:ui-graphics`, `androidx.compose.material3:material3`
- `androidx.lifecycle:lifecycle-runtime-ktx:2.8.2`
- `androidx.security:security-crypto:1.1.0-alpha06` — EncryptedSharedPreferences (Keystore).
- `org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1` — IO threading for RPC.

**Deliberately NOT used**
- No okhttp / retrofit — networking is `java.net.HttpURLConnection` (`NodeClient.kt`).
- No ZXing / QR lib — a tiny pure-Kotlin QR encoder is inlined (`QrCode.kt`, MIT, Project Nayuki).
- No JSON lib — Android's built-in `org.json`.
- No Material Components (`com.google.android.material`) — Compose Material3 + a platform NoActionBar theme.

**Build tooling**
- Android Gradle Plugin `8.5.2`, Kotlin `1.9.24`, Compose compiler ext `1.5.14`,
  `compileSdk 34`, `minSdk 24`, JDK 17.
