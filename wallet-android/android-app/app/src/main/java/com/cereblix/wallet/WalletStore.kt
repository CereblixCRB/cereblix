package com.cereblix.wallet

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import mobile.Mobile
import org.json.JSONObject

/**
 * WalletStore is the single owner of the private key on the device.
 *
 *  - Key generation, address derivation and transaction SIGNING are delegated to
 *    the gomobile AAR ([Mobile]) -> the SAME cereblix/core crypto as the desktop
 *    wallet (ed25519, crb1 addresses, the cerebra-tx-v1 / ChainID signing payload).
 *  - The 128-hex private key is stored in [EncryptedSharedPreferences], i.e.
 *    AES-256 encrypted with a master key held in the hardware-backed Android
 *    Keystore. The plaintext key is read only to hand to [Mobile.signSend] or on
 *    an explicit export, and is NEVER sent over the network.
 */
class WalletStore(context: Context) {

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
    }

    fun hasWallet(): Boolean = prefs.contains(KEY_PRIV)

    fun address(): String? = prefs.getString(KEY_ADDR, null)

    /** synapses-per-CRB, sourced from core via the binding so units never drift. */
    fun coinUnit(): Long = Mobile.coinUnit()

    fun endpoint(): String = prefs.getString(KEY_ENDPOINT, DEFAULT_ENDPOINT) ?: DEFAULT_ENDPOINT

    fun setEndpoint(url: String) {
        prefs.edit().putString(KEY_ENDPOINT, url.trim().trimEnd('/')).apply()
    }

    /** Create a brand-new wallet. Returns the new crb1 address. */
    fun createWallet(): String {
        val j = JSONObject(Mobile.newAddress())
        if (j.has("error")) throw IllegalStateException(j.getString("error"))
        val addr = j.getString("addr")
        save(j.getString("priv"), addr)
        return addr
    }

    /** Import an existing 128-hex ed25519 key. Returns the address, or null if invalid. */
    fun importWallet(privHex: String): String? {
        val addr = Mobile.addressFromPriv(privHex.trim())
        if (addr.isEmpty()) return null
        save(privHex.trim(), addr)
        return addr
    }

    /** The only place the raw private key is exposed to the UI (explicit export). */
    fun exportPrivateKey(): String? = prefs.getString(KEY_PRIV, null)

    fun validateAddress(addr: String): Boolean = Mobile.validateAddress(addr)

    /**
     * Sign a payment fully locally and return the signed core.Tx JSON ready to
     * POST to /tx. Amounts are in synapses. Returns a JSON string that is either
     * the signed tx (has "sig") or {"error":"..."}.
     */
    fun signSend(to: String, amountSyn: Long, feeSyn: Long, nonce: Long, height: Long): String {
        val priv = prefs.getString(KEY_PRIV, null) ?: return """{"error":"no key in wallet"}"""
        return Mobile.signSend(priv, to, amountSyn, feeSyn, nonce, height)
    }

    /** Wipe the wallet (key + address) from the encrypted store. */
    fun reset() {
        prefs.edit().remove(KEY_PRIV).remove(KEY_ADDR).apply()
    }

    private fun save(priv: String, addr: String) {
        prefs.edit().putString(KEY_PRIV, priv).putString(KEY_ADDR, addr).apply()
    }

    companion object {
        const val DEFAULT_ENDPOINT = "https://cereblix.com/api"
        private const val KEY_PRIV = "priv"
        private const val KEY_ADDR = "addr"
        private const val KEY_ENDPOINT = "endpoint"
    }
}
