package com.cereblix.wallet

import android.content.Context
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import androidx.fragment.app.FragmentActivity
import mobile.Mobile
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/** This build's version. The signed update manifest is offered only when it
 *  advertises a strictly-newer semver. */
const val APP_VERSION = "1.0.0"

// --------------------------------------------------------------------- biometric

/**
 * Thin wrapper over androidx.biometric. Gates the app-open, every payment
 * broadcast, and private-key reveal/export behind a BiometricPrompt that ALSO
 * accepts the device PIN/pattern/password (DEVICE_CREDENTIAL fallback).
 *
 * If the device has no biometric AND no screen lock enrolled, [prompt] proceeds
 * (so the user is never locked out of their own funds) — the UI surfaces a
 * "lock unavailable" warning so the user can enable a screen lock.
 */
object BiometricGate {

    private val authenticators =
        BiometricManager.Authenticators.BIOMETRIC_WEAK or
            BiometricManager.Authenticators.DEVICE_CREDENTIAL

    /** True when a biometric or device credential can actually be used to authenticate. */
    fun available(ctx: Context): Boolean =
        BiometricManager.from(ctx).canAuthenticate(authenticators) == BiometricManager.BIOMETRIC_SUCCESS

    fun prompt(
        activity: FragmentActivity?,
        title: String,
        subtitle: String,
        onSuccess: () -> Unit,
        onCancel: () -> Unit = {},
    ) {
        if (activity == null || !available(activity)) {
            // No enrolled authenticator (or no host activity): allow, but the
            // caller is expected to show a "biometric lock unavailable" warning.
            onSuccess()
            return
        }
        val prompt = BiometricPrompt(
            activity,
            ContextCompat.getMainExecutor(activity),
            object : BiometricPrompt.AuthenticationCallback() {
                override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                    onSuccess()
                }

                override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
                    onCancel()
                }
            },
        )
        val info = BiometricPrompt.PromptInfo.Builder()
            .setTitle(title)
            .setSubtitle(subtitle)
            // DEVICE_CREDENTIAL is in the allowed set, so no negative button is set.
            .setAllowedAuthenticators(authenticators)
            .build()
        prompt.authenticate(info)
    }
}

// ------------------------------------------------------------------ update check

/** Parsed, signature-verified update offer. */
data class UpdateInfo(val version: String, val notes: String, val url: String)

/**
 * Fetches the authority-SIGNED update manifest and returns an [UpdateInfo] only
 * when (a) the ed25519 signature over the payload verifies against the embedded
 * public key, AND (b) the advertised version is strictly newer than [APP_VERSION].
 * Any failure — network, bad signature, malformed JSON, missing android URL —
 * yields null, so the app simply shows no banner (fail-closed).
 *
 * Manifest envelope:  {"payload":"<json-string>","sig":"<hex>"}
 * Verified payload:   {"version":"x.y.z","notes":"…","platforms":{"android":{"url":"…"}}}
 */
object UpdateChecker {

    private const val MANIFEST_URL = "https://cereblix.com/wallet/latest.json"

    // ed25519 public key (hex) that signs the wallet update manifest.
    private const val MANIFEST_PUBKEY =
        "de9a9336d692524da0c248a5de8fb01d4f88487d1411868320a3e3ea1be0d32d"

    fun check(): UpdateInfo? {
        return try {
            val envelopeRaw = fetch(MANIFEST_URL) ?: return null
            val envelope = JSONObject(envelopeRaw)
            val payload = envelope.optString("payload")
            val sig = envelope.optString("sig")
            if (payload.isEmpty() || sig.isEmpty()) return null

            // Authenticate the manifest BEFORE trusting any field in it.
            if (!Mobile.verifyManifest(MANIFEST_PUBKEY, payload, sig)) return null

            val p = JSONObject(payload)
            val version = p.optString("version")
            if (version.isEmpty() || !semverNewer(version, APP_VERSION)) return null
            val url = p.optJSONObject("platforms")?.optJSONObject("android")?.optString("url").orEmpty()
            if (url.isEmpty()) return null
            UpdateInfo(version = version, notes = p.optString("notes"), url = url)
        } catch (e: Exception) {
            null
        }
    }

    private fun fetch(urlStr: String): String? {
        val conn = URL(urlStr).openConnection() as HttpURLConnection
        return try {
            conn.connectTimeout = 5_000
            conn.readTimeout = 5_000
            conn.requestMethod = "GET"
            conn.setRequestProperty("Accept", "application/json")
            if (conn.responseCode !in 200..299) {
                null
            } else {
                conn.inputStream.bufferedReader().use { it.readText() }
            }
        } catch (e: Exception) {
            null
        } finally {
            conn.disconnect()
        }
    }
}

/** True iff dotted-numeric semver [a] is strictly greater than [b] (e.g. 1.2.0 > 1.1.9). */
fun semverNewer(a: String, b: String): Boolean {
    val pa = a.trim().removePrefix("v").split(".")
    val pb = b.trim().removePrefix("v").split(".")
    val n = maxOf(pa.size, pb.size)
    for (i in 0 until n) {
        val x = pa.getOrNull(i)?.takeWhile { it.isDigit() }?.toIntOrNull() ?: 0
        val y = pb.getOrNull(i)?.takeWhile { it.isDigit() }?.toIntOrNull() ?: 0
        if (x != y) return x > y
    }
    return false
}
