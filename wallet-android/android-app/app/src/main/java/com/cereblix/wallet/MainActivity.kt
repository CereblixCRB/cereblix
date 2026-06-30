package com.cereblix.wallet

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Divider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val store = WalletStore(applicationContext)
        setContent { CereblixTheme { AppRoot(store) } }
    }
}

// --------------------------------------------------------------------- theme

@Composable
private fun CereblixTheme(content: @Composable () -> Unit) {
    val colors = darkColorScheme(
        primary = Color(0xFF6EE7B7),
        onPrimary = Color(0xFF062A20),
        background = Color(0xFF0B0F14),
        surface = Color(0xFF131A22),
        onSurface = Color(0xFFE6EDF3),
    )
    MaterialTheme(colorScheme = colors, content = content)
}

// ------------------------------------------------------------------ unit fmt

private const val DECIMALS = 8

private fun fmtCrb(syn: Long): String {
    val unit = 100_000_000L
    val whole = syn / unit
    val frac = syn % unit
    var s = "%d.%08d".format(whole, frac)
    s = s.trimEnd('0').trimEnd('.')
    return if (s.isEmpty()) "0" else s
}

/** Parse a CRB decimal string to integer synapses, or null if malformed. */
private fun toSyn(input: String): Long? {
    val unit = 100_000_000L
    val t = input.trim()
    if (t.isEmpty()) return null
    val parts = t.split(".")
    if (parts.size > 2) return null
    val w = (parts[0].ifEmpty { "0" }).toLongOrNull() ?: return null
    if (w < 0) return null
    var frac = if (parts.size == 2) parts[1] else ""
    if (frac.length > DECIMALS) return null
    frac = frac.padEnd(DECIMALS, '0')
    val f = if (frac.isEmpty()) 0L else frac.toLongOrNull() ?: return null
    return w * unit + f
}

// ------------------------------------------------------------------ app root

@Composable
private fun AppRoot(store: WalletStore) {
    var hasWallet by remember { mutableStateOf(store.hasWallet()) }
    if (!hasWallet) {
        OnboardingScreen(store) { hasWallet = true }
    } else {
        MainScaffold(store) { hasWallet = false }
    }
}

private enum class Tab(val label: String) {
    DASHBOARD("Wallet"), SEND("Send"), RECEIVE("Receive"), HISTORY("History"), SETTINGS("Settings")
}

@Composable
private fun MainScaffold(store: WalletStore, onReset: () -> Unit) {
    var tab by remember { mutableStateOf(Tab.DASHBOARD) }
    Scaffold(
        bottomBar = {
            NavigationBar {
                Tab.entries.forEach { t ->
                    NavigationBarItem(
                        selected = tab == t,
                        onClick = { tab = t },
                        icon = { Text(t.label.take(1)) },
                        label = { Text(t.label, fontSize = 11.sp) },
                    )
                }
            }
        },
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            when (tab) {
                Tab.DASHBOARD -> DashboardScreen(store)
                Tab.SEND -> SendScreen(store)
                Tab.RECEIVE -> ReceiveScreen(store)
                Tab.HISTORY -> HistoryScreen(store)
                Tab.SETTINGS -> SettingsScreen(store, onReset)
            }
        }
    }
}

// ----------------------------------------------------------------- onboarding

@Composable
private fun OnboardingScreen(store: WalletStore, onCreated: () -> Unit) {
    val scope = rememberCoroutineScope()
    var importing by remember { mutableStateOf(false) }
    var privInput by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }

    ScreenColumn {
        Spacer(Modifier.height(40.dp))
        Text("Cereblix", fontSize = 34.sp, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.primary)
        Text("Self-custody CRB wallet", color = MaterialTheme.colorScheme.onSurface)
        Spacer(Modifier.height(40.dp))

        if (!importing) {
            Button(onClick = {
                error = null
                scope.launch {
                    try {
                        store.createWallet()
                        onCreated()
                    } catch (e: Exception) {
                        error = e.message
                    }
                }
            }, modifier = Modifier.fillMaxWidth()) { Text("Create a new wallet") }
            Spacer(Modifier.height(12.dp))
            OutlinedButton(onClick = { importing = true }, modifier = Modifier.fillMaxWidth()) {
                Text("Import an existing key")
            }
        } else {
            OutlinedTextField(
                value = privInput,
                onValueChange = { privInput = it },
                label = { Text("128-hex private key") },
                modifier = Modifier.fillMaxWidth(),
                singleLine = true,
            )
            Spacer(Modifier.height(12.dp))
            Button(onClick = {
                error = null
                val addr = store.importWallet(privInput)
                if (addr == null) error = "Invalid private key (need 128 hex chars)" else onCreated()
            }, modifier = Modifier.fillMaxWidth()) { Text("Import") }
            Spacer(Modifier.height(8.dp))
            OutlinedButton(onClick = { importing = false }, modifier = Modifier.fillMaxWidth()) { Text("Back") }
        }
        error?.let { ErrorText(it) }
    }
}

// ------------------------------------------------------------------ dashboard

@Composable
private fun DashboardScreen(store: WalletStore) {
    val addr = store.address() ?: ""
    var balance by remember { mutableStateOf<Long?>(null) }
    var height by remember { mutableStateOf<Long?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var refresh by remember { mutableStateOf(0) }

    LaunchedEffect(refresh) {
        error = null
        try {
            val client = NodeClient(store.endpoint())
            val (b, s) = withContext(Dispatchers.IO) { client.balance(addr) to client.status() }
            balance = b.balance
            height = s.height
        } catch (e: Exception) {
            error = e.message ?: "network error"
        }
    }

    ScreenColumn {
        Text("Balance", color = MaterialTheme.colorScheme.onSurface)
        Spacer(Modifier.height(4.dp))
        Row(verticalAlignment = Alignment.Bottom) {
            Text(
                balance?.let { fmtCrb(it) } ?: "—",
                fontSize = 40.sp,
                fontWeight = FontWeight.Bold,
                color = MaterialTheme.colorScheme.primary,
            )
            Spacer(Modifier.height(0.dp))
            Text("  CRB", fontSize = 18.sp, color = MaterialTheme.colorScheme.onSurface)
        }
        Spacer(Modifier.height(16.dp))
        Card(Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp)) {
                Text("Your address", fontWeight = FontWeight.SemiBold)
                Text(addr, fontFamily = FontFamily.Monospace, fontSize = 12.sp)
            }
        }
        Spacer(Modifier.height(16.dp))
        Text("Network height: " + (height?.toString() ?: "—"), color = MaterialTheme.colorScheme.onSurface)
        Spacer(Modifier.height(16.dp))
        Button(onClick = { refresh++ }, modifier = Modifier.fillMaxWidth()) { Text("Refresh") }
        error?.let { ErrorText(it) }
    }
}

// ----------------------------------------------------------------------- send

@Composable
private fun SendScreen(store: WalletStore) {
    val scope = rememberCoroutineScope()
    var to by remember { mutableStateOf("") }
    var amount by remember { mutableStateOf("") }
    var fee by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var result by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }

    ScreenColumn {
        Text("Send CRB", fontSize = 24.sp, fontWeight = FontWeight.Bold)
        Spacer(Modifier.height(16.dp))
        OutlinedTextField(to, { to = it }, label = { Text("To (crb1...)") }, singleLine = true, modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(8.dp))
        OutlinedTextField(
            amount, { amount = it }, label = { Text("Amount (CRB)") }, singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Decimal),
            modifier = Modifier.fillMaxWidth(),
        )
        Spacer(Modifier.height(8.dp))
        OutlinedTextField(
            fee, { fee = it }, label = { Text("Fee (CRB, blank = suggested)") }, singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Decimal),
            modifier = Modifier.fillMaxWidth(),
        )
        Spacer(Modifier.height(16.dp))
        Button(
            enabled = !busy,
            onClick = {
                error = null; result = null
                if (!store.validateAddress(to)) { error = "Invalid destination address"; return@Button }
                val amt = toSyn(amount) ?: run { error = "Invalid amount"; return@Button }
                if (amt <= 0) { error = "Amount must be > 0"; return@Button }
                busy = true
                scope.launch {
                    try {
                        val txid = withContext(Dispatchers.IO) {
                            val client = NodeClient(store.endpoint())
                            val feeSyn = if (fee.isBlank()) client.suggestedFee()
                                else toSyn(fee) ?: throw IllegalArgumentException("Invalid fee")
                            val bal = client.balance(store.address()!!)
                            val height = client.nextHeight()
                            val signed = store.signSend(to, amt, feeSyn, bal.nonce, height)
                            val j = JSONObject(signed)
                            if (j.has("error")) throw IllegalStateException(j.getString("error"))
                            client.broadcast(signed)
                        }
                        result = txid
                    } catch (e: Exception) {
                        error = e.message ?: "send failed"
                    } finally {
                        busy = false
                    }
                }
            },
            modifier = Modifier.fillMaxWidth(),
        ) { if (busy) CircularProgressIndicator(Modifier.height(20.dp)) else Text("Sign locally & broadcast") }

        result?.let {
            Spacer(Modifier.height(16.dp))
            Card(Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp)) {
                    Text("Broadcast OK", color = MaterialTheme.colorScheme.primary, fontWeight = FontWeight.Bold)
                    Text("txid:", fontWeight = FontWeight.SemiBold)
                    Text(it, fontFamily = FontFamily.Monospace, fontSize = 12.sp)
                }
            }
        }
        error?.let { ErrorText(it) }
    }
}

// -------------------------------------------------------------------- receive

@Composable
private fun ReceiveScreen(store: WalletStore) {
    val addr = store.address() ?: ""
    val ctx = androidx.compose.ui.platform.LocalContext.current
    ScreenColumn {
        Text("Receive CRB", fontSize = 24.sp, fontWeight = FontWeight.Bold)
        Spacer(Modifier.height(16.dp))
        Surface(color = Color.White, modifier = Modifier.fillMaxWidth(0.8f).aspectRatio(1f)) {
            QrImage(addr, Modifier.fillMaxSize().padding(8.dp))
        }
        Spacer(Modifier.height(16.dp))
        Text(addr, fontFamily = FontFamily.Monospace, fontSize = 13.sp)
        Spacer(Modifier.height(16.dp))
        Button(onClick = { copyToClipboard(ctx, addr) }, modifier = Modifier.fillMaxWidth()) {
            Text("Copy address")
        }
    }
}

// -------------------------------------------------------------------- history

@Composable
private fun HistoryScreen(store: WalletStore) {
    val addr = store.address() ?: ""
    var items by remember { mutableStateOf<List<NodeClient.HistoryItem>?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var refresh by remember { mutableStateOf(0) }

    LaunchedEffect(refresh) {
        error = null
        try {
            items = withContext(Dispatchers.IO) { NodeClient(store.endpoint()).history(addr) }
        } catch (e: Exception) {
            error = e.message ?: "network error"
        }
    }

    val df = remember { SimpleDateFormat("MM-dd HH:mm", Locale.US) }
    ScreenColumn {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
            Text("History", fontSize = 24.sp, fontWeight = FontWeight.Bold)
            OutlinedButton(onClick = { refresh++ }) { Text("Refresh") }
        }
        Spacer(Modifier.height(8.dp))
        when {
            error != null -> ErrorText(error!!)
            items == null -> Text("Loading…")
            items!!.isEmpty() -> Text("No transactions yet.")
            else -> items!!.forEach { h ->
                val incoming = h.to == addr
                Divider()
                Row(Modifier.fillMaxWidth().padding(vertical = 8.dp), horizontalArrangement = Arrangement.SpaceBetween) {
                    Column(Modifier.fillMaxWidth(0.7f)) {
                        Text(
                            (if (incoming) "Received" else "Sent") + " • #" + h.height,
                            fontWeight = FontWeight.SemiBold,
                            color = if (incoming) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurface,
                        )
                        Text(
                            (if (incoming) "from " + h.from else "to " + h.to),
                            fontSize = 11.sp, fontFamily = FontFamily.Monospace,
                            maxLines = 1, overflow = TextOverflow.Ellipsis,
                        )
                        Text(df.format(Date(h.time * 1000)), fontSize = 11.sp)
                    }
                    Text((if (incoming) "+" else "-") + fmtCrb(h.amount), fontWeight = FontWeight.Bold)
                }
            }
        }
    }
}

// -------------------------------------------------------------------- settings

@Composable
private fun SettingsScreen(store: WalletStore, onReset: () -> Unit) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    var endpoint by remember { mutableStateOf(store.endpoint()) }
    var revealed by remember { mutableStateOf(false) }

    ScreenColumn {
        Text("Settings", fontSize = 24.sp, fontWeight = FontWeight.Bold)
        Spacer(Modifier.height(16.dp))
        Text("Node RPC endpoint", fontWeight = FontWeight.SemiBold)
        OutlinedTextField(endpoint, { endpoint = it }, singleLine = true, modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(8.dp))
        Button(onClick = {
            store.setEndpoint(endpoint)
            Toast.makeText(ctx, "Saved", Toast.LENGTH_SHORT).show()
        }, modifier = Modifier.fillMaxWidth()) { Text("Save endpoint") }

        Spacer(Modifier.height(24.dp))
        Divider()
        Spacer(Modifier.height(16.dp))
        Text("Backup", fontWeight = FontWeight.SemiBold)
        Text("Your private key is stored encrypted in the Android Keystore and never leaves the device.", fontSize = 12.sp)
        Spacer(Modifier.height(8.dp))
        if (!revealed) {
            OutlinedButton(onClick = { revealed = true }, modifier = Modifier.fillMaxWidth()) { Text("Reveal private key") }
        } else {
            Card(Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp)) {
                    Text(store.exportPrivateKey() ?: "(none)", fontFamily = FontFamily.Monospace, fontSize = 11.sp)
                    Spacer(Modifier.height(8.dp))
                    Button(onClick = { copyToClipboard(ctx, store.exportPrivateKey() ?: "") }) { Text("Copy") }
                }
            }
        }

        Spacer(Modifier.height(24.dp))
        Divider()
        Spacer(Modifier.height(16.dp))
        OutlinedButton(
            onClick = { store.reset(); onReset() },
            modifier = Modifier.fillMaxWidth(),
        ) { Text("Remove wallet from this device", color = Color(0xFFFF6B6B)) }
        Text("Make sure you have backed up your key first.", fontSize = 11.sp)
    }
}

// --------------------------------------------------------------------- shared

@Composable
private fun ScreenColumn(content: @Composable androidx.compose.foundation.layout.ColumnScope.() -> Unit) {
    Column(
        Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(20.dp),
        content = content,
    )
}

@Composable
private fun ErrorText(msg: String) {
    Spacer(Modifier.height(12.dp))
    Text(msg, color = Color(0xFFFF6B6B))
}

private fun copyToClipboard(ctx: Context, text: String) {
    val cm = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
    cm.setPrimaryClip(ClipData.newPlainText("cereblix", text))
    Toast.makeText(ctx, "Copied", Toast.LENGTH_SHORT).show()
}
