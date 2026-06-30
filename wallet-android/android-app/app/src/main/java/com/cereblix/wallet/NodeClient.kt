package com.cereblix.wallet

import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder

/**
 * Thin Cereblix node RPC client over java.net.HttpURLConnection (NO okhttp).
 *
 * Endpoints used (base like https://cereblix.com/api):
 *   GET  /balance?addr=  -> {balance, nonce}
 *   GET  /status         -> {height, hashrate, supply, reward, mempool, peers, fee_suggested, ...}
 *   GET  /history?addr=&limit=  -> [{txid,height,time,from,to,amount,fee}, ...]
 *   POST /tx  (signed core.Tx JSON)  -> {txid}
 *
 * All amounts are uint64 synapses (carried here as Long). Every call BLOCKS, so
 * run it on Dispatchers.IO (see MainActivity).
 */
class NodeClient(baseUrl: String) {

    private val base: String = baseUrl.trim().trimEnd('/')

    data class Balance(val balance: Long, val nonce: Long)

    data class Status(
        val height: Long,
        val hashrate: Double,
        val supply: Long,
        val reward: Long,
        val mempool: Long,
        val peers: Long,
        val feeSuggested: Long,
    )

    data class HistoryItem(
        val txid: String,
        val height: Long,
        val time: Long,
        val from: String,
        val to: String,
        val amount: Long,
        val fee: Long,
    )

    /** A transaction's location + fields, from GET /tx?id= — used to drive RBF. */
    data class TxLocation(
        val found: Boolean,
        val pending: Boolean,
        val txid: String,
        val from: String,
        val to: String,
        val amount: Long,
        val fee: Long,
        val nonce: Long,
        val coinbase: Boolean,
    )

    fun balance(addr: String): Balance {
        val o = getJson("/balance?addr=" + enc(addr))
        return Balance(o.optLong("balance"), o.optLong("nonce"))
    }

    fun status(): Status {
        val o = getJson("/status")
        return Status(
            height = o.optLong("height"),
            hashrate = o.optDouble("hashrate", 0.0),
            supply = o.optLong("supply"),
            reward = o.optLong("reward"),
            mempool = o.optLong("mempool"),
            peers = o.optLong("peers"),
            feeSuggested = o.optLong("fee_suggested"),
        )
    }

    /** Height the next tx would be mined at (tip + 1); used to pick the signing payload. */
    fun nextHeight(): Long = status().height + 1

    fun suggestedFee(): Long {
        val f = status().feeSuggested
        return if (f > 0) f else 1000L // fallback floor 0.00001 CRB
    }

    fun history(addr: String, limit: Int = 25): List<HistoryItem> {
        val arr = getArray("/history?addr=" + enc(addr) + "&limit=" + limit)
        val out = ArrayList<HistoryItem>(arr.length())
        for (i in 0 until arr.length()) {
            val o = arr.getJSONObject(i)
            out.add(
                HistoryItem(
                    txid = o.optString("txid"),
                    height = o.optLong("height"),
                    time = o.optLong("time"),
                    from = o.optString("from"),
                    to = o.optString("to"),
                    amount = o.optLong("amount"),
                    fee = o.optLong("fee"),
                ),
            )
        }
        return out
    }

    /** Look up a transaction by id (confirmed or pending). Drives RBF speed-up/cancel. */
    fun txLocation(txid: String): TxLocation {
        val o = getJson("/tx?id=" + enc(txid))
        return TxLocation(
            found = o.optBoolean("found"),
            pending = o.optBoolean("pending"),
            txid = o.optString("txid"),
            from = o.optString("from"),
            to = o.optString("to"),
            amount = o.optLong("amount"),
            fee = o.optLong("fee"),
            nonce = o.optLong("nonce"),
            coinbase = o.optBoolean("coinbase"),
        )
    }

    /** POST a signed tx JSON (from WalletStore.signSend). Returns the txid. */
    fun broadcast(signedTxJson: String): String {
        val o = postJson("/tx", signedTxJson)
        if (o.has("error")) throw IOException(o.getString("error"))
        return o.optString("txid")
    }

    // ----------------------------------------------------------------- internals

    private fun getJson(path: String): JSONObject {
        val body = httpGet(path)
        val o = JSONObject(body)
        if (o.has("error")) throw IOException(o.getString("error"))
        return o
    }

    private fun getArray(path: String): JSONArray {
        val body = httpGet(path)
        // /history returns a JSON array; an error comes back as an object.
        val trimmed = body.trimStart()
        if (trimmed.startsWith("{")) {
            val o = JSONObject(body)
            if (o.has("error")) throw IOException(o.getString("error"))
        }
        return JSONArray(body)
    }

    private fun httpGet(path: String): String {
        val conn = open(path)
        conn.requestMethod = "GET"
        return read(conn)
    }

    private fun postJson(path: String, json: String): JSONObject {
        val conn = open(path)
        conn.requestMethod = "POST"
        conn.doOutput = true
        conn.setRequestProperty("Content-Type", "application/json")
        conn.outputStream.use { it.write(json.toByteArray(Charsets.UTF_8)) }
        return JSONObject(read(conn))
    }

    private fun open(path: String): HttpURLConnection {
        val conn = URL(base + path).openConnection() as HttpURLConnection
        conn.connectTimeout = 15_000
        conn.readTimeout = 20_000
        conn.setRequestProperty("Accept", "application/json")
        return conn
    }

    private fun read(conn: HttpURLConnection): String {
        return try {
            val stream = if (conn.responseCode in 200..299) conn.inputStream else conn.errorStream
            stream?.bufferedReader()?.use { it.readText() } ?: ""
        } finally {
            conn.disconnect()
        }
    }

    private fun enc(s: String): String = URLEncoder.encode(s, "UTF-8")
}
