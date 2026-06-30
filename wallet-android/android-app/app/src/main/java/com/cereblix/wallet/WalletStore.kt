package com.cereblix.wallet

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import mobile.Mobile
import org.json.JSONArray
import org.json.JSONObject

/**
 * WalletStore is the single owner of the private keys on the device.
 *
 *  - Key generation, address derivation and transaction SIGNING are delegated to
 *    the gomobile AAR ([Mobile]) -> the SAME cereblix/core crypto as the desktop
 *    wallet (ed25519, crb1 addresses, the cerebra-tx-v1 / ChainID signing payload),
 *    so a key created here imports into the desktop / CLI wallet and back.
 *  - The wallet now holds a LIST of accounts (label, address, 128-hex private key)
 *    serialised as a JSON blob in [EncryptedSharedPreferences] -> AES-256 encrypted
 *    with a master key in the hardware-backed Android Keystore. Plaintext keys are
 *    read only to hand to [Mobile.signSend] or on an explicit (biometric-gated)
 *    export, and are NEVER logged or sent over the network.
 *  - A wallet written by the previous single-key build is MIGRATED to the list
 *    format transparently on first load (see [migrateLegacy]).
 */
class WalletStore(context: Context) {

    /** One wallet account. [privHex] is empty in the public [addresses] view. */
    data class Account(val label: String, val addr: String, val privHex: String)

    private val prefs: SharedPreferences

    init {
        val masterKey = MasterKey.Builder(context)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        prefs = EncryptedSharedPreferences.create(
            context,
            "cereblix_wallet",
            masterKey,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
        )
        migrateLegacy()
    }

    // ------------------------------------------------------------------ query

    fun hasWallet(): Boolean = rawAccounts().length() > 0

    /** Every account WITH its private key (internal use: signing / export). */
    fun listAccounts(): List<Account> {
        val arr = rawAccounts()
        val out = ArrayList<Account>(arr.length())
        for (i in 0 until arr.length()) {
            val o = arr.getJSONObject(i)
            out.add(Account(o.optString("label"), o.optString("addr"), o.optString("priv")))
        }
        return out
    }

    /** Public view for the UI: labels + addresses only, no private material. */
    fun addresses(): List<Account> = listAccounts().map { Account(it.label, it.addr, "") }

    /** First address — convenience for single-address call sites. */
    fun address(): String? = rawAccounts().let { if (it.length() == 0) null else it.getJSONObject(0).optString("addr") }

    fun ownsAddress(addr: String): Boolean = listAccounts().any { it.addr == addr }

    /** synapses-per-CRB, sourced from core via the binding so units never drift. */
    fun coinUnit(): Long = Mobile.coinUnit()

    fun endpoint(): String = prefs.getString(KEY_ENDPOINT, DEFAULT_ENDPOINT) ?: DEFAULT_ENDPOINT

    fun setEndpoint(url: String) {
        prefs.edit().putString(KEY_ENDPOINT, url.trim().trimEnd('/')).apply()
    }

    fun validateAddress(addr: String): Boolean = Mobile.validateAddress(addr)

    // ----------------------------------------------------------------- mutate

    /**
     * Generate a brand-new account. Returns the new account INCLUDING the private
     * key so a fresh wallet can present a one-time backup screen; the key is also
     * persisted immediately so it can never be lost.
     */
    fun createAccount(label: String): Account {
        val j = JSONObject(Mobile.newAddress())
        if (j.has("error")) throw IllegalStateException(j.getString("error"))
        val addr = j.getString("addr")
        val priv = j.getString("priv")
        val lbl = uniqueLabel(if (label.isBlank()) "addr-${rawAccounts().length() + 1}" else label.trim())
        appendAccount(lbl, addr, priv)
        return Account(lbl, addr, priv)
    }

    /**
     * Import an existing 128-hex ed25519 key. Returns the account (no private key
     * in the result), or null if the key is invalid or already in the wallet.
     */
    fun importAccount(privHex: String, label: String): Account? {
        val clean = privHex.trim()
        val addr = Mobile.addressFromPriv(clean)
        if (addr.isEmpty()) return null
        if (ownsAddress(addr)) return null
        val lbl = uniqueLabel(if (label.isBlank()) "imported" else label.trim())
        appendAccount(lbl, addr, clean)
        return Account(lbl, addr, "")
    }

    /** Reveal one account's private key (the UI gates this behind BiometricPrompt). */
    fun exportPrivateKey(addr: String): String? =
        listAccounts().firstOrNull { it.addr == addr }?.privHex

    /**
     * Sign a payment fully locally from [fromAddr]'s key and return the signed
     * core.Tx JSON ready to POST to /tx. Amounts are in synapses. Returns either
     * the signed tx (has "sig") or {"error":"..."}.
     */
    fun signSend(fromAddr: String, to: String, amountSyn: Long, feeSyn: Long, nonce: Long, height: Long): String {
        val priv = listAccounts().firstOrNull { it.addr == fromAddr }?.privHex
            ?: return """{"error":"no key for that address in wallet"}"""
        return Mobile.signSend(priv, to, amountSyn, feeSyn, nonce, height)
    }

    /** Remove a single account. */
    fun removeAccount(addr: String) {
        val keep = JSONArray()
        val arr = rawAccounts()
        for (i in 0 until arr.length()) {
            val o = arr.getJSONObject(i)
            if (o.optString("addr") != addr) keep.put(o)
        }
        prefs.edit().putString(KEY_ACCOUNTS, keep.toString()).apply()
    }

    /** Wipe the entire wallet (all accounts) from the encrypted store. */
    fun reset() {
        prefs.edit().remove(KEY_ACCOUNTS).remove(KEY_PRIV).remove(KEY_ADDR).apply()
    }

    // -------------------------------------------------------------- internals

    private fun rawAccounts(): JSONArray {
        val s = prefs.getString(KEY_ACCOUNTS, null) ?: return JSONArray()
        return try {
            JSONArray(s)
        } catch (e: Exception) {
            JSONArray()
        }
    }

    private fun appendAccount(label: String, addr: String, priv: String) {
        val arr = rawAccounts()
        arr.put(JSONObject().put("label", label).put("addr", addr).put("priv", priv))
        prefs.edit().putString(KEY_ACCOUNTS, arr.toString()).apply()
    }

    /** Ensure a label is unique by suffixing -2, -3, ... when it collides. */
    private fun uniqueLabel(base: String): String {
        val existing = listAccounts().map { it.label }.toHashSet()
        if (base !in existing) return base
        var n = 2
        while ("$base-$n" in existing) n++
        return "$base-$n"
    }

    /** Upgrade a pre-multi-key wallet (single "priv"/"addr" pair) to the list. */
    private fun migrateLegacy() {
        if (prefs.contains(KEY_ACCOUNTS)) return
        val oldPriv = prefs.getString(KEY_PRIV, null)
        val oldAddr = prefs.getString(KEY_ADDR, null)
        if (oldPriv.isNullOrEmpty() || oldAddr.isNullOrEmpty()) return
        val arr = JSONArray()
        arr.put(JSONObject().put("label", "main").put("addr", oldAddr).put("priv", oldPriv))
        prefs.edit()
            .putString(KEY_ACCOUNTS, arr.toString())
            .remove(KEY_PRIV)
            .remove(KEY_ADDR)
            .apply()
    }

    companion object {
        const val DEFAULT_ENDPOINT = "https://cereblix.com/api"
        private const val KEY_ACCOUNTS = "accounts"
        private const val KEY_PRIV = "priv" // legacy single-key field (migrated away)
        private const val KEY_ADDR = "addr" // legacy single-key field (migrated away)
        private const val KEY_ENDPOINT = "endpoint"
    }
}
