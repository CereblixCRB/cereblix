package com.cereblix.wallet

import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.content.ContextWrapper
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.PersistableBundle
import android.view.WindowManager
import android.widget.Toast
import androidx.activity.compose.setContent
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.AccountBalanceWallet
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.CallReceived
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.filled.Fingerprint
import androidx.compose.material.icons.filled.History
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Send
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Visibility
import androidx.compose.material.icons.filled.VisibilityOff
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationBarItemDefaults
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.Typography
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
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.core.view.WindowCompat
import androidx.fragment.app.FragmentActivity
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

class MainActivity : FragmentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // SECURITY: block screenshots / screen-recording and blank the recents
        // thumbnail so the private-key reveal + import + backup screens can't leak
        // the key.
        window.setFlags(
            WindowManager.LayoutParams.FLAG_SECURE,
            WindowManager.LayoutParams.FLAG_SECURE,
        )

        // Brand the system bars: navy background, light (white) icons.
        window.statusBarColor = 0xFF06080F.toInt()
        window.navigationBarColor = 0xFF06080F.toInt()
        WindowCompat.getInsetsController(window, window.decorView).apply {
            isAppearanceLightStatusBars = false
            isAppearanceLightNavigationBars = false
        }

        val store = WalletStore(applicationContext)
        setContent { CereblixTheme { AppRoot(store) } }
    }
}

// --------------------------------------------------------------------- brand

/** Cereblix brand palette + signature gradient (matches the desktop wallet). */
private object Brand {
    val Bg = Color(0xFF06080F)        // very dark navy canvas
    val Panel = Color(0xFF0E1424)     // raised surface
    val Glass = Color(0x0D92B4FF)     // translucent white-blue (~5%)
    val Glass2 = Color(0x1492B4FF)    // translucent white-blue (~8%)
    val Border = Color(0x2192B4FF)    // 1px hairline (~13%)
    val Border2 = Color(0x5978C8FF)   // stronger line (~35%)
    val Cyan = Color(0xFF22D3EE)
    val Violet = Color(0xFFA78BFA)
    val Sky = Color(0xFF38BDF8)
    val Txt = Color(0xFFE8ECF6)       // primary text
    val Mut = Color(0xFF90A0BD)       // muted text
    val Faint = Color(0xFF61708C)     // faint labels
    val Ok = Color(0xFF34D399)        // received / positive
    val Bad = Color(0xFFF87171)       // sent / danger
    val Warn = Color(0xFFFBBF24)      // caution
    val Ink = Color(0xFF06121F)       // dark ink on gradient buttons
    val Field = Color(0x8C060A14)     // input fill

    /** THE signature cyan -> violet diagonal gradient. */
    val gradient = Brush.linearGradient(listOf(Cyan, Violet))
}

// ---- bundled fonts (Space Grotesk for display/headings/numbers, Inter for body)
private val SpaceGrotesk = FontFamily(
    Font(R.font.space_grotesk_medium, FontWeight.Medium),
    Font(R.font.space_grotesk_bold, FontWeight.Bold),
)
private val Inter = FontFamily(
    Font(R.font.inter_regular, FontWeight.Normal),
    Font(R.font.inter_medium, FontWeight.Medium),
    Font(R.font.inter_bold, FontWeight.Bold),
)

private fun cereblixTypography(): Typography {
    val b = Typography()
    return b.copy(
        bodyLarge = b.bodyLarge.copy(fontFamily = Inter),
        bodyMedium = b.bodyMedium.copy(fontFamily = Inter),
        bodySmall = b.bodySmall.copy(fontFamily = Inter),
        labelLarge = b.labelLarge.copy(fontFamily = SpaceGrotesk, fontWeight = FontWeight.Medium),
        labelMedium = b.labelMedium.copy(fontFamily = SpaceGrotesk),
        labelSmall = b.labelSmall.copy(fontFamily = SpaceGrotesk),
        titleLarge = b.titleLarge.copy(fontFamily = SpaceGrotesk, fontWeight = FontWeight.Bold),
        titleMedium = b.titleMedium.copy(fontFamily = SpaceGrotesk),
        titleSmall = b.titleSmall.copy(fontFamily = SpaceGrotesk),
        headlineLarge = b.headlineLarge.copy(fontFamily = SpaceGrotesk, fontWeight = FontWeight.Bold),
        headlineMedium = b.headlineMedium.copy(fontFamily = SpaceGrotesk, fontWeight = FontWeight.Bold),
        headlineSmall = b.headlineSmall.copy(fontFamily = SpaceGrotesk, fontWeight = FontWeight.Bold),
        displayLarge = b.displayLarge.copy(fontFamily = SpaceGrotesk, fontWeight = FontWeight.Bold),
        displayMedium = b.displayMedium.copy(fontFamily = SpaceGrotesk, fontWeight = FontWeight.Bold),
        displaySmall = b.displaySmall.copy(fontFamily = SpaceGrotesk, fontWeight = FontWeight.Bold),
    )
}

@Composable
private fun CereblixTheme(content: @Composable () -> Unit) {
    val colors = darkColorScheme(
        primary = Brand.Cyan,
        onPrimary = Brand.Ink,
        secondary = Brand.Violet,
        onSecondary = Brand.Ink,
        tertiary = Brand.Sky,
        background = Brand.Bg,
        onBackground = Brand.Txt,
        surface = Brand.Panel,
        onSurface = Brand.Txt,
        surfaceVariant = Brand.Panel,
        onSurfaceVariant = Brand.Mut,
        error = Brand.Bad,
        onError = Brand.Ink,
        outline = Brand.Border2,
    )
    MaterialTheme(colorScheme = colors, typography = cereblixTypography(), content = content)
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

private fun fmtHashrate(h: Double): String {
    if (h <= 0) return "0 H/s"
    val units = arrayOf("H/s", "kH/s", "MH/s", "GH/s", "TH/s")
    var v = h
    var i = 0
    while (v >= 1000.0 && i < units.size - 1) {
        v /= 1000.0
        i++
    }
    return if (i == 0) "%.0f %s".format(v, units[i]) else "%.2f %s".format(v, units[i])
}

/** crb1abcd…wxyz — middle-truncate a long address for compact display. */
private fun shortMid(s: String, head: Int = 10, tail: Int = 8): String =
    if (s.length <= head + tail + 1) s else s.take(head) + "…" + s.takeLast(tail)

/** Walk the Context chain to the hosting FragmentActivity (for BiometricPrompt). */
private fun Context.findActivity(): FragmentActivity? {
    var c: Context? = this
    while (c is ContextWrapper) {
        if (c is FragmentActivity) return c
        c = c.baseContext
    }
    return null
}

/** Best-effort balance fetch for a set of accounts (blocking; call on Dispatchers.IO). */
private fun loadBalances(client: NodeClient, accounts: List<WalletStore.Account>): Map<String, Long> {
    val m = HashMap<String, Long>(accounts.size)
    for (a in accounts) {
        try {
            m[a.addr] = client.balance(a.addr).balance
        } catch (_: Exception) {
            // leave unset; UI shows "—"
        }
    }
    return m
}

// ------------------------------------------------------------------ app root

@Composable
private fun AppRoot(store: WalletStore) {
    val ctx = LocalContext.current
    val activity = remember { ctx.findActivity() }
    val bioAvailable = remember { activity != null && BiometricGate.available(ctx) }

    var hasWallet by remember { mutableStateOf(store.hasWallet()) }
    // Lock the app behind biometrics on open when one is enrolled.
    var unlocked by remember { mutableStateOf(!(hasWallet && bioAvailable)) }

    var update by remember { mutableStateOf<UpdateInfo?>(null) }
    var updateDismissed by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) {
        update = withContext(Dispatchers.IO) { UpdateChecker.check() }
    }

    AuroraBackground {
        when {
            !hasWallet -> OnboardingScreen(store, bioAvailable) {
                hasWallet = true
                unlocked = true
            }
            !unlocked -> LockScreen(activity) { unlocked = true }
            else -> MainScaffold(
                store = store,
                update = if (updateDismissed) null else update,
                onDismissUpdate = { updateDismissed = true },
            ) {
                store.reset()
                hasWallet = false
                unlocked = true
            }
        }
    }
}

private enum class Tab(val label: String, val icon: ImageVector) {
    DASHBOARD("Wallet", Icons.Filled.AccountBalanceWallet),
    SEND("Send", Icons.Filled.Send),
    RECEIVE("Receive", Icons.Filled.CallReceived),
    HISTORY("History", Icons.Filled.History),
    SETTINGS("Settings", Icons.Filled.Settings),
}

@Composable
private fun MainScaffold(
    store: WalletStore,
    update: UpdateInfo?,
    onDismissUpdate: () -> Unit,
    onReset: () -> Unit,
) {
    var tab by remember { mutableStateOf(Tab.DASHBOARD) }
    Scaffold(
        containerColor = Color.Transparent,
        topBar = { BrandTopBar() },
        bottomBar = {
            NavigationBar(containerColor = Brand.Panel, tonalElevation = 0.dp) {
                Tab.entries.forEach { t ->
                    NavigationBarItem(
                        selected = tab == t,
                        onClick = { tab = t },
                        icon = { Icon(t.icon, contentDescription = t.label, modifier = Modifier.size(22.dp)) },
                        label = { Text(t.label, fontSize = 11.sp, fontFamily = SpaceGrotesk) },
                        colors = NavigationBarItemDefaults.colors(
                            selectedIconColor = Brand.Cyan,
                            selectedTextColor = Brand.Cyan,
                            indicatorColor = Brand.Cyan.copy(alpha = 0.14f),
                            unselectedIconColor = Brand.Mut,
                            unselectedTextColor = Brand.Mut,
                        ),
                    )
                }
            }
        },
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            update?.let { UpdateBanner(it, onDismissUpdate) }
            Box(Modifier.weight(1f).fillMaxWidth()) {
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
}

// --------------------------------------------------------------------- lock

@Composable
private fun LockScreen(activity: FragmentActivity?, onUnlocked: () -> Unit) {
    fun auth() = BiometricGate.prompt(
        activity,
        title = "Unlock Cereblix",
        subtitle = "Confirm it's you to open your wallet",
        onSuccess = onUnlocked,
        onCancel = {},
    )
    LaunchedEffect(Unit) { auth() }
    Column(
        Modifier.fillMaxSize().padding(24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        BrandLogo(logo = 64.dp)
        Spacer(Modifier.height(18.dp))
        Wordmark(fontSize = 28.sp)
        Spacer(Modifier.height(10.dp))
        Text("Wallet locked", color = Brand.Mut, fontFamily = Inter, fontSize = 14.sp)
        Spacer(Modifier.height(28.dp))
        GradientButton(
            text = "Unlock",
            modifier = Modifier.widthIn(max = 280.dp).fillMaxWidth(),
            icon = Icons.Filled.Fingerprint,
        ) { auth() }
    }
}

// ----------------------------------------------------------------- onboarding

@Composable
private fun OnboardingScreen(store: WalletStore, bioAvailable: Boolean, onCreated: () -> Unit) {
    val scope = rememberCoroutineScope()
    var mode by remember { mutableStateOf("choice") } // choice | import | backup
    var newAccount by remember { mutableStateOf<WalletStore.Account?>(null) }
    var privInput by remember { mutableStateOf("") }
    var labelInput by remember { mutableStateOf("") }
    var showKey by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    Column(
        Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Column(
            Modifier.widthIn(max = 460.dp).fillMaxWidth(),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            BrandLogo(logo = 58.dp)
            Spacer(Modifier.height(16.dp))
            Wordmark(fontSize = 30.sp)
            Spacer(Modifier.height(8.dp))
            Text("Self-custody CRB wallet", color = Brand.Mut, fontFamily = Inter, fontSize = 14.sp)
            Spacer(Modifier.height(32.dp))

            when (mode) {
                "choice" -> GlassCard(accentTop = true, modifier = Modifier.fillMaxWidth()) {
                    Text(
                        "Get started",
                        color = Brand.Txt,
                        fontFamily = SpaceGrotesk,
                        fontWeight = FontWeight.Bold,
                        fontSize = 18.sp,
                    )
                    Spacer(Modifier.height(4.dp))
                    Text(
                        "Create a fresh wallet or restore one from an existing private key. Your keys are encrypted in the Android Keystore and never leave this device.",
                        color = Brand.Mut,
                        fontFamily = Inter,
                        fontSize = 13.sp,
                    )
                    Spacer(Modifier.height(20.dp))
                    GradientButton(
                        text = if (busy) "Creating…" else "Create a new wallet",
                        modifier = Modifier.fillMaxWidth(),
                        icon = Icons.Filled.Add,
                        enabled = !busy,
                        loading = busy,
                    ) {
                        error = null
                        busy = true
                        scope.launch {
                            try {
                                val acct = withContext(Dispatchers.IO) { store.createAccount("main") }
                                newAccount = acct
                                showKey = false
                                mode = "backup"
                            } catch (e: Exception) {
                                error = e.message
                            } finally {
                                busy = false
                            }
                        }
                    }
                    Spacer(Modifier.height(12.dp))
                    SecondaryButton(
                        text = "Import an existing key",
                        modifier = Modifier.fillMaxWidth(),
                    ) { error = null; mode = "import" }
                    error?.let { ErrorBanner(it) }
                }

                "import" -> GlassCard(accentTop = true, modifier = Modifier.fillMaxWidth()) {
                    Text(
                        "Import a key",
                        color = Brand.Txt,
                        fontFamily = SpaceGrotesk,
                        fontWeight = FontWeight.Bold,
                        fontSize = 18.sp,
                    )
                    Spacer(Modifier.height(16.dp))
                    BrandTextField(
                        value = privInput,
                        onValueChange = { privInput = it },
                        label = "128-hex private key",
                        modifier = Modifier.fillMaxWidth(),
                        keyboardOptions = KeyboardOptions(
                            keyboardType = KeyboardType.Password,
                            autoCorrect = false,
                        ),
                        visualTransformation =
                            if (showKey) VisualTransformation.None else PasswordVisualTransformation(),
                        trailingIcon = {
                            Icon(
                                imageVector = if (showKey) Icons.Filled.VisibilityOff else Icons.Filled.Visibility,
                                contentDescription = if (showKey) "Hide key" else "Show key",
                                tint = Brand.Mut,
                                modifier = Modifier.size(20.dp).clickable { showKey = !showKey },
                            )
                        },
                    )
                    Spacer(Modifier.height(12.dp))
                    BrandTextField(
                        value = labelInput,
                        onValueChange = { labelInput = it },
                        label = "Label (optional)",
                        modifier = Modifier.fillMaxWidth(),
                    )
                    Spacer(Modifier.height(16.dp))
                    GradientButton(text = "Import", modifier = Modifier.fillMaxWidth()) {
                        error = null
                        val acct = store.importAccount(privInput, labelInput)
                        if (acct == null) {
                            error = "Invalid or duplicate private key (need 128 hex chars)"
                        } else {
                            onCreated()
                        }
                    }
                    Spacer(Modifier.height(10.dp))
                    SecondaryButton(text = "Back", modifier = Modifier.fillMaxWidth()) {
                        error = null; mode = "choice"
                    }
                    error?.let { ErrorBanner(it) }
                }

                "backup" -> BackupCard(
                    privHex = newAccount?.privHex ?: "",
                    bioAvailable = bioAvailable,
                    onDone = onCreated,
                )
            }
        }
    }
}

/** Backup-at-creation: show the new key, require an explicit "I saved it" confirm. */
@Composable
private fun BackupCard(privHex: String, bioAvailable: Boolean, onDone: () -> Unit) {
    val ctx = LocalContext.current
    var show by remember { mutableStateOf(false) }
    var saved by remember { mutableStateOf(false) }

    GlassCard(accentTop = true, modifier = Modifier.fillMaxWidth()) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Icon(Icons.Filled.Lock, contentDescription = null, tint = Brand.Cyan, modifier = Modifier.size(18.dp))
            Spacer(Modifier.width(8.dp))
            Text(
                "Back up your key",
                color = Brand.Txt,
                fontFamily = SpaceGrotesk,
                fontWeight = FontWeight.Bold,
                fontSize = 18.sp,
            )
        }
        Spacer(Modifier.height(6.dp))
        Text(
            "This is the ONLY copy of your private key. Write it down and store it somewhere safe — anyone with it controls your funds, and it can never be recovered if lost.",
            color = Brand.Mut,
            fontFamily = Inter,
            fontSize = 13.sp,
        )
        Spacer(Modifier.height(14.dp))
        Box(
            Modifier
                .fillMaxWidth()
                .clip(RoundedCornerShape(12.dp))
                .background(Brand.Field)
                .border(1.dp, Brand.Border2, RoundedCornerShape(12.dp))
                .padding(14.dp),
        ) {
            Text(
                if (show) privHex else "•".repeat(64),
                fontFamily = FontFamily.Monospace,
                fontSize = 12.sp,
                color = Brand.Cyan,
            )
        }
        Spacer(Modifier.height(10.dp))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            SecondaryButton(
                text = if (show) "Hide" else "Reveal",
                modifier = Modifier.weight(1f),
                icon = if (show) Icons.Filled.VisibilityOff else Icons.Filled.Visibility,
            ) { show = !show }
            SecondaryButton(
                text = "Copy",
                modifier = Modifier.weight(1f),
                icon = Icons.Filled.ContentCopy,
            ) { copyPrivateKey(ctx, privHex) }
        }
        Spacer(Modifier.height(14.dp))
        Row(
            Modifier.fillMaxWidth().clickable { saved = !saved },
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Checkbox(
                checked = saved,
                onCheckedChange = { saved = it },
                colors = CheckboxDefaults.colors(
                    checkedColor = Brand.Cyan,
                    uncheckedColor = Brand.Border2,
                    checkmarkColor = Brand.Ink,
                ),
            )
            Spacer(Modifier.width(4.dp))
            Text(
                "I have written down my private key",
                color = Brand.Txt,
                fontFamily = Inter,
                fontSize = 13.sp,
            )
        }
        Spacer(Modifier.height(8.dp))
        GradientButton(
            text = "Enter wallet",
            modifier = Modifier.fillMaxWidth(),
            enabled = saved,
        ) { if (saved) onDone() }
        if (!bioAvailable) {
            Spacer(Modifier.height(10.dp))
            Text(
                "Tip: set a screen lock (PIN / fingerprint) to enable biometric protection for this wallet.",
                color = Brand.Warn,
                fontFamily = Inter,
                fontSize = 11.sp,
            )
        }
    }
}

// ------------------------------------------------------------------ dashboard

@Composable
private fun DashboardScreen(store: WalletStore) {
    val ctx = LocalContext.current
    val accounts = remember { store.addresses() }
    var balances by remember { mutableStateOf<Map<String, Long>>(emptyMap()) }
    var status by remember { mutableStateOf<NodeClient.Status?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var loaded by remember { mutableStateOf(false) }
    var refresh by remember { mutableStateOf(0) }

    LaunchedEffect(refresh) {
        error = null
        try {
            val client = NodeClient(store.endpoint())
            val (b, s) = withContext(Dispatchers.IO) { loadBalances(client, accounts) to client.status() }
            balances = b
            status = s
            loaded = true
        } catch (e: Exception) {
            error = e.message ?: "network error"
        }
    }

    val total = accounts.sumOf { balances[it.addr] ?: 0L }

    ScreenColumn {
        BalanceHero(if (loaded) fmtCrb(total) else "—")

        Spacer(Modifier.height(20.dp))
        BrandLabel("Your addresses")
        Spacer(Modifier.height(10.dp))
        GlassCard(modifier = Modifier.fillMaxWidth(), padding = 8.dp) {
            accounts.forEachIndexed { i, a ->
                if (i > 0) BrandDivider()
                AddressRow(a.label, a.addr, balances[a.addr]) { copyToClipboard(ctx, a.addr) }
            }
        }

        Spacer(Modifier.height(20.dp))
        BrandLabel("Network")
        Spacer(Modifier.height(10.dp))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            StatChip("Height", status?.height?.toString() ?: "—", Modifier.weight(1f))
            StatChip("Supply", status?.let { fmtCrb(it.supply) } ?: "—", Modifier.weight(1f))
        }
        Spacer(Modifier.height(10.dp))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            StatChip("Hashrate", status?.let { fmtHashrate(it.hashrate) } ?: "—", Modifier.weight(1f))
            StatChip("Reward", status?.let { fmtCrb(it.reward) } ?: "—", Modifier.weight(1f))
        }
        Spacer(Modifier.height(10.dp))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            StatChip("Mempool", status?.mempool?.toString() ?: "—", Modifier.weight(1f))
            StatChip("Peers", status?.peers?.toString() ?: "—", Modifier.weight(1f))
        }

        Spacer(Modifier.height(20.dp))
        GradientButton(
            text = "Refresh",
            modifier = Modifier.fillMaxWidth(),
            icon = Icons.Filled.Refresh,
        ) { refresh++ }
        error?.let { ErrorBanner(it) }
    }
}

@Composable
private fun AddressRow(label: String, addr: String, balance: Long?, onCopy: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().padding(vertical = 10.dp, horizontal = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(
                label,
                fontFamily = SpaceGrotesk,
                fontWeight = FontWeight.Medium,
                fontSize = 14.sp,
                color = Brand.Txt,
            )
            Text(
                shortMid(addr, 14, 10),
                fontFamily = FontFamily.Monospace,
                fontSize = 11.sp,
                color = Brand.Mut,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
        Spacer(Modifier.width(8.dp))
        Text(
            balance?.let { fmtCrb(it) } ?: "—",
            fontFamily = FontFamily.Monospace,
            fontWeight = FontWeight.Bold,
            fontSize = 14.sp,
            color = Brand.Txt,
        )
        Spacer(Modifier.width(10.dp))
        CopyIconButton(onCopy)
    }
}

@Composable
private fun BalanceHero(balanceStr: String) {
    val shape = RoundedCornerShape(20.dp)
    Column(
        Modifier
            .fillMaxWidth()
            .clip(shape)
            .background(Brand.Glass2)
            .drawBehind {
                drawRect(
                    Brush.radialGradient(
                        colors = listOf(Color(0x24A78BFA), Color.Transparent),
                        center = Offset(size.width, 0f),
                        radius = size.width * 0.95f,
                    ),
                )
                drawRect(
                    Brush.radialGradient(
                        colors = listOf(Color(0x1A22D3EE), Color.Transparent),
                        center = Offset(0f, size.height),
                        radius = size.width * 0.95f,
                    ),
                )
            }
            .border(1.dp, Brand.Border2, shape),
    ) {
        Box(Modifier.fillMaxWidth().height(2.dp).background(Brand.gradient))
        Column(Modifier.padding(22.dp)) {
            BrandLabel("Total balance")
            Spacer(Modifier.height(6.dp))
            Row(verticalAlignment = Alignment.Bottom) {
                Text(
                    balanceStr,
                    style = TextStyle(
                        brush = Brand.gradient,
                        fontFamily = SpaceGrotesk,
                        fontWeight = FontWeight.Bold,
                        fontSize = 42.sp,
                        letterSpacing = (-1).sp,
                    ),
                )
                Spacer(Modifier.width(8.dp))
                Text(
                    "CRB",
                    color = Brand.Cyan,
                    fontFamily = SpaceGrotesk,
                    fontWeight = FontWeight.Medium,
                    fontSize = 18.sp,
                    modifier = Modifier.padding(bottom = 4.dp),
                )
            }
        }
    }
}

// ----------------------------------------------------------------------- send

@Composable
private fun SendScreen(store: WalletStore) {
    val ctx = LocalContext.current
    val activity = remember { ctx.findActivity() }
    val scope = rememberCoroutineScope()
    val accounts = remember { store.addresses() }

    var balances by remember { mutableStateOf<Map<String, Long>>(emptyMap()) }
    var suggestedFee by remember { mutableStateOf<Long?>(null) }

    var from by remember { mutableStateOf("") } // "" = auto
    var to by remember { mutableStateOf("") }
    var amount by remember { mutableStateOf("") }
    var feeOverride by remember { mutableStateOf("") }
    var advanced by remember { mutableStateOf(false) }

    var step by remember { mutableStateOf("form") } // form | review | success
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var txid by remember { mutableStateOf<String?>(null) }
    var rbfMsg by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(Unit) {
        try {
            val client = NodeClient(store.endpoint())
            val (b, f) = withContext(Dispatchers.IO) { loadBalances(client, accounts) to client.suggestedFee() }
            balances = b
            suggestedFee = f
        } catch (_: Exception) {
        }
    }

    fun feeSynForReview(): Long = if (advanced && feeOverride.isNotBlank()) {
        toSyn(feeOverride) ?: 0L
    } else {
        suggestedFee ?: 0L
    }

    fun doSend() {
        error = null
        BiometricGate.prompt(
            activity,
            title = "Authorize payment",
            subtitle = "Confirm to sign and broadcast",
            onSuccess = {
                busy = true
                scope.launch {
                    try {
                        val id = withContext(Dispatchers.IO) {
                            val client = NodeClient(store.endpoint())
                            val amt = toSyn(amount) ?: throw IllegalArgumentException("Invalid amount")
                            val feeSyn = if (advanced && feeOverride.isNotBlank()) {
                                toSyn(feeOverride) ?: throw IllegalArgumentException("Invalid fee")
                            } else {
                                client.suggestedFee()
                            }
                            val fromAddr = if (from.isNotEmpty()) {
                                from
                            } else {
                                accounts.firstOrNull { client.balance(it.addr).balance >= amt + feeSyn }?.addr
                                    ?: throw IllegalStateException("No single address has enough balance (pick one or top up)")
                            }
                            val height = client.nextHeight()
                            val nonce = client.balance(fromAddr).nonce
                            val signed = store.signSend(fromAddr, to.trim(), amt, feeSyn, nonce, height)
                            val j = JSONObject(signed)
                            if (j.has("error")) throw IllegalStateException(j.getString("error"))
                            client.broadcast(signed)
                        }
                        txid = id
                        rbfMsg = null
                        step = "success"
                    } catch (e: Exception) {
                        error = e.message ?: "send failed"
                        step = "form"
                    } finally {
                        busy = false
                    }
                }
            },
            onCancel = { error = "Authentication cancelled" },
        )
    }

    fun rbf(cancel: Boolean) {
        error = null
        rbfMsg = null
        BiometricGate.prompt(
            activity,
            title = if (cancel) "Authorize cancel" else "Authorize speed-up",
            subtitle = "Confirm to broadcast the replacement",
            onSuccess = {
                busy = true
                scope.launch {
                    try {
                        val newId = withContext(Dispatchers.IO) {
                            val client = NodeClient(store.endpoint())
                            val loc = client.txLocation(txid ?: throw IllegalStateException("no transaction"))
                            if (loc.coinbase) throw IllegalStateException("Cannot replace a coinbase transaction")
                            if (!loc.pending) throw IllegalStateException("Already confirmed — it can no longer be replaced")
                            if (!store.ownsAddress(loc.from)) throw IllegalStateException("Sender is not in this wallet")
                            // Clear the node's replace-by-fee bar: old fee + 10% (min +1).
                            var minFee = loc.fee + loc.fee / 10
                            if (minFee <= loc.fee) minFee = loc.fee + 1
                            var fee = minFee
                            val sug = client.suggestedFee()
                            if (sug > fee) fee = sug
                            val height = client.nextHeight()
                            val dTo = if (cancel) loc.from else loc.to
                            val dAmt = if (cancel) 1L else loc.amount
                            val signed = store.signSend(loc.from, dTo, dAmt, fee, loc.nonce, height)
                            val j = JSONObject(signed)
                            if (j.has("error")) throw IllegalStateException(j.getString("error"))
                            client.broadcast(signed)
                        }
                        txid = newId
                        rbfMsg = if (cancel) "Cancellation broadcast" else "Speed-up broadcast"
                    } catch (e: Exception) {
                        error = e.message ?: "replace failed"
                    } finally {
                        busy = false
                    }
                }
            },
            onCancel = { error = "Authentication cancelled" },
        )
    }

    ScreenColumn {
        ScreenTitle("Send CRB")
        Spacer(Modifier.height(16.dp))

        when (step) {
            "form" -> {
                val toValid = to.isNotBlank() && store.validateAddress(to.trim())
                GlassCard(modifier = Modifier.fillMaxWidth()) {
                    BrandLabel("From")
                    Spacer(Modifier.height(8.dp))
                    AddressPicker(
                        accounts = accounts,
                        selected = from,
                        allowAuto = true,
                        balances = balances,
                    ) { from = it }
                    Spacer(Modifier.height(14.dp))
                    BrandTextField(
                        to, { to = it }, "Recipient address (crb1…)", Modifier.fillMaxWidth(),
                        trailingIcon = recipientTrailingIcon(to.isBlank(), toValid),
                    )
                    Spacer(Modifier.height(12.dp))
                    BrandTextField(
                        amount, { amount = it }, "Amount (CRB)", Modifier.fillMaxWidth(),
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Decimal),
                    )
                    Spacer(Modifier.height(12.dp))
                    Row(
                        Modifier.fillMaxWidth().clickable { advanced = !advanced },
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Icon(Icons.Filled.Settings, contentDescription = null, tint = Brand.Mut, modifier = Modifier.size(16.dp))
                        Spacer(Modifier.width(8.dp))
                        Text(
                            "Advanced — custom fee",
                            color = Brand.Mut,
                            fontFamily = SpaceGrotesk,
                            fontSize = 13.sp,
                        )
                    }
                    if (advanced) {
                        Spacer(Modifier.height(10.dp))
                        BrandTextField(
                            feeOverride, { feeOverride = it },
                            "Fee (CRB · blank = auto, next block)", Modifier.fillMaxWidth(),
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Decimal),
                        )
                    } else {
                        Spacer(Modifier.height(6.dp))
                        Text(
                            "Auto fee" + (suggestedFee?.let { " · next block ≈ " + fmtCrb(it) + " CRB" } ?: ""),
                            color = Brand.Faint,
                            fontFamily = Inter,
                            fontSize = 11.sp,
                        )
                    }
                }
                Spacer(Modifier.height(16.dp))
                GradientButton(
                    text = "Review",
                    modifier = Modifier.fillMaxWidth(),
                    icon = Icons.Filled.Send,
                ) {
                    error = null
                    if (!store.validateAddress(to.trim())) {
                        error = "Invalid destination address"; return@GradientButton
                    }
                    val amt = toSyn(amount)
                    if (amt == null || amt <= 0) {
                        error = "Invalid amount"; return@GradientButton
                    }
                    if (advanced && feeOverride.isNotBlank() && toSyn(feeOverride) == null) {
                        error = "Invalid fee"; return@GradientButton
                    }
                    step = "review"
                }
            }

            "review" -> {
                val amt = toSyn(amount) ?: 0L
                val feeSyn = feeSynForReview()
                GlassCard(accentTop = true, modifier = Modifier.fillMaxWidth()) {
                    Text(
                        "Review transaction",
                        color = Brand.Txt,
                        fontFamily = SpaceGrotesk,
                        fontWeight = FontWeight.Bold,
                        fontSize = 16.sp,
                    )
                    Spacer(Modifier.height(12.dp))
                    ReviewLine("From", if (from.isEmpty()) "Auto — funded address" else shortMid(from, 12, 8))
                    ReviewLine("To", shortMid(to.trim(), 12, 8))
                    ReviewLine("Amount", fmtCrb(amt) + " CRB")
                    ReviewLine(
                        "Network fee",
                        fmtCrb(feeSyn) + " CRB" + if (!advanced || feeOverride.isBlank()) " (auto)" else "",
                    )
                    BrandDivider()
                    ReviewLine("Total", fmtCrb(amt + feeSyn) + " CRB", emphasize = true)
                }
                Spacer(Modifier.height(12.dp))
                Box(
                    Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(10.dp))
                        .background(Brand.Warn.copy(alpha = 0.10f))
                        .border(1.dp, Brand.Warn.copy(alpha = 0.30f), RoundedCornerShape(10.dp))
                        .padding(12.dp),
                ) {
                    Text(
                        "Transactions are irreversible. Double-check the recipient address.",
                        color = Brand.Warn,
                        fontFamily = Inter,
                        fontSize = 12.sp,
                    )
                }
                Spacer(Modifier.height(16.dp))
                GradientButton(
                    text = if (busy) "Broadcasting…" else "Confirm & send",
                    modifier = Modifier.fillMaxWidth(),
                    enabled = !busy,
                    loading = busy,
                    icon = Icons.Filled.Send,
                ) { doSend() }
                Spacer(Modifier.height(10.dp))
                SecondaryButton(text = "Back", modifier = Modifier.fillMaxWidth(), enabled = !busy) {
                    if (!busy) step = "form"
                }
            }

            "success" -> {
                GlassCard(accentTop = true, modifier = Modifier.fillMaxWidth()) {
                    Text(
                        "Broadcast OK",
                        color = Brand.Ok,
                        fontFamily = SpaceGrotesk,
                        fontWeight = FontWeight.Bold,
                        fontSize = 16.sp,
                    )
                    rbfMsg?.let {
                        Spacer(Modifier.height(4.dp))
                        Text(it, color = Brand.Cyan, fontFamily = Inter, fontSize = 12.sp)
                    }
                    Spacer(Modifier.height(8.dp))
                    BrandLabel("txid")
                    Spacer(Modifier.height(4.dp))
                    Text(txid ?: "", fontFamily = FontFamily.Monospace, fontSize = 12.sp, color = Brand.Txt)
                    Spacer(Modifier.height(8.dp))
                    CopyIconButton { copyToClipboard(ctx, txid ?: "") }
                }
                Spacer(Modifier.height(14.dp))
                Text(
                    "Still pending? You can replace it:",
                    color = Brand.Mut,
                    fontFamily = Inter,
                    fontSize = 12.sp,
                )
                Spacer(Modifier.height(8.dp))
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    SecondaryButton(
                        text = "Speed up",
                        modifier = Modifier.weight(1f),
                        icon = Icons.Filled.Refresh,
                    ) { if (!busy) rbf(cancel = false) }
                    SecondaryButton(
                        text = "Cancel tx",
                        modifier = Modifier.weight(1f),
                        icon = Icons.Filled.Close,
                        danger = true,
                    ) { if (!busy) rbf(cancel = true) }
                }
                Spacer(Modifier.height(16.dp))
                GradientButton(text = "Send another", modifier = Modifier.fillMaxWidth()) {
                    to = ""; amount = ""; feeOverride = ""; advanced = false
                    error = null; txid = null; rbfMsg = null; step = "form"
                }
            }
        }
        error?.let { ErrorBanner(it) }
    }
}

/** Trailing valid/invalid mark for the recipient field; null while it is empty. */
private fun recipientTrailingIcon(empty: Boolean, valid: Boolean): (@Composable () -> Unit)? {
    if (empty) return null
    return {
        Icon(
            if (valid) Icons.Filled.Check else Icons.Filled.Close,
            contentDescription = if (valid) "Valid" else "Invalid",
            tint = if (valid) Brand.Ok else Brand.Bad,
            modifier = Modifier.size(20.dp),
        )
    }
}

@Composable
private fun ReviewLine(k: String, v: String, emphasize: Boolean = false) {
    Row(
        Modifier.fillMaxWidth().padding(vertical = 7.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(k, color = if (emphasize) Brand.Txt else Brand.Mut, fontFamily = Inter, fontSize = 13.sp)
        Text(
            v,
            color = if (emphasize) Brand.Cyan else Brand.Txt,
            fontFamily = if (emphasize) SpaceGrotesk else FontFamily.Monospace,
            fontWeight = if (emphasize) FontWeight.Bold else FontWeight.Normal,
            fontSize = 13.sp,
        )
    }
}

// -------------------------------------------------------------------- receive

@Composable
private fun ReceiveScreen(store: WalletStore) {
    val ctx = LocalContext.current
    var accountsVersion by remember { mutableStateOf(0) }
    val accounts = remember(accountsVersion) { store.addresses() }
    var selected by remember { mutableStateOf("") }
    var showAdd by remember { mutableStateOf(false) }
    var newLabel by remember { mutableStateOf("") }

    val addr = if (selected.isNotEmpty() && accounts.any { it.addr == selected }) {
        selected
    } else {
        accounts.firstOrNull()?.addr ?: ""
    }

    ScreenColumn(horizontalAlignment = Alignment.CenterHorizontally) {
        ScreenTitle("Receive CRB")
        Spacer(Modifier.height(8.dp))
        Text(
            "Choose an address, then scan or share it to receive CRB.",
            color = Brand.Mut,
            fontFamily = Inter,
            fontSize = 13.sp,
        )
        Spacer(Modifier.height(16.dp))
        AddressPicker(
            accounts = accounts,
            selected = addr,
            allowAuto = false,
            balances = emptyMap(),
            modifier = Modifier.widthIn(max = 420.dp),
        ) { selected = it }

        Spacer(Modifier.height(20.dp))
        Box(
            Modifier
                .size(248.dp)
                .clip(RoundedCornerShape(18.dp))
                .background(Brand.gradient)
                .padding(2.dp)
                .clip(RoundedCornerShape(16.dp))
                .background(Color.White)
                .padding(14.dp),
        ) {
            if (addr.isNotEmpty()) QrImage(addr, Modifier.fillMaxSize())
        }
        Spacer(Modifier.height(20.dp))
        GlassCard(modifier = Modifier.fillMaxWidth()) {
            BrandLabel("Selected address")
            Spacer(Modifier.height(6.dp))
            Text(addr, fontFamily = FontFamily.Monospace, fontSize = 13.sp, color = Brand.Txt)
        }
        Spacer(Modifier.height(16.dp))
        GradientButton(
            text = "Copy address",
            modifier = Modifier.fillMaxWidth(),
            icon = Icons.Filled.ContentCopy,
        ) { copyToClipboard(ctx, addr) }
        Spacer(Modifier.height(10.dp))
        SecondaryButton(
            text = "Add a new address",
            modifier = Modifier.fillMaxWidth(),
            icon = Icons.Filled.Add,
        ) { newLabel = ""; showAdd = true }
    }

    if (showAdd) {
        Dialog(onDismissRequest = { showAdd = false }) {
            GlassCard(accentTop = true, opaque = true, modifier = Modifier.fillMaxWidth()) {
                Text(
                    "New address",
                    color = Brand.Txt,
                    fontFamily = SpaceGrotesk,
                    fontWeight = FontWeight.Bold,
                    fontSize = 16.sp,
                )
                Spacer(Modifier.height(6.dp))
                Text(
                    "Generates a fresh ed25519 key on this device and adds it to your wallet.",
                    color = Brand.Mut,
                    fontFamily = Inter,
                    fontSize = 12.sp,
                )
                Spacer(Modifier.height(14.dp))
                BrandTextField(newLabel, { newLabel = it }, "Label (optional)", Modifier.fillMaxWidth())
                Spacer(Modifier.height(14.dp))
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    SecondaryButton(text = "Cancel", modifier = Modifier.weight(1f)) { showAdd = false }
                    GradientButton(text = "Create", modifier = Modifier.weight(1f)) {
                        val a = store.createAccount(newLabel)
                        selected = a.addr
                        accountsVersion++
                        showAdd = false
                    }
                }
            }
        }
    }
}

// -------------------------------------------------------------------- history

@Composable
private fun HistoryScreen(store: WalletStore) {
    val accounts = remember { store.addresses() }
    val ourAddrs = remember(accounts) { accounts.map { it.addr }.toHashSet() }
    var items by remember { mutableStateOf<List<NodeClient.HistoryItem>?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var refresh by remember { mutableStateOf(0) }

    LaunchedEffect(refresh) {
        error = null
        try {
            items = withContext(Dispatchers.IO) {
                val client = NodeClient(store.endpoint())
                val seen = HashSet<String>()
                val all = ArrayList<NodeClient.HistoryItem>()
                for (a in accounts) {
                    for (h in client.history(a.addr)) {
                        if (seen.add(h.txid)) all.add(h)
                    }
                }
                all.sortedByDescending { it.height }
            }
        } catch (e: Exception) {
            error = e.message ?: "network error"
        }
    }

    val df = remember { SimpleDateFormat("MM-dd HH:mm", Locale.US) }
    ScreenColumn {
        Row(
            Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically,
        ) {
            ScreenTitle("History")
            SecondaryButton(text = "Refresh", icon = Icons.Filled.Refresh) { refresh++ }
        }
        Spacer(Modifier.height(12.dp))
        when {
            error != null -> ErrorBanner(error!!)
            items == null -> Text("Loading…", color = Brand.Mut, fontFamily = Inter)
            items!!.isEmpty() ->
                GlassCard(modifier = Modifier.fillMaxWidth()) {
                    Text(
                        "No transactions yet.",
                        color = Brand.Mut,
                        fontFamily = Inter,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
            else ->
                GlassCard(modifier = Modifier.fillMaxWidth(), padding = 8.dp) {
                    items!!.forEachIndexed { i, h ->
                        if (i > 0) BrandDivider()
                        TxRow(h, ourAddrs.contains(h.to), df)
                    }
                }
        }
    }
}

@Composable
private fun TxRow(h: NodeClient.HistoryItem, incoming: Boolean, df: SimpleDateFormat) {
    val accent = if (incoming) Brand.Ok else Brand.Bad
    Row(
        Modifier.fillMaxWidth().padding(vertical = 10.dp, horizontal = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(
            Modifier
                .size(40.dp)
                .clip(RoundedCornerShape(12.dp))
                .background(accent.copy(alpha = 0.10f))
                .border(1.dp, accent.copy(alpha = 0.28f), RoundedCornerShape(12.dp)),
            contentAlignment = Alignment.Center,
        ) {
            Icon(
                if (incoming) Icons.Filled.ArrowDownward else Icons.Filled.ArrowUpward,
                contentDescription = if (incoming) "Received" else "Sent",
                tint = accent,
                modifier = Modifier.size(20.dp),
            )
        }
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(
                (if (incoming) "Received" else "Sent") + "  ·  #" + h.height,
                fontFamily = SpaceGrotesk,
                fontWeight = FontWeight.Medium,
                fontSize = 14.sp,
                color = Brand.Txt,
            )
            Text(
                if (incoming) "from " + h.from else "to " + h.to,
                fontSize = 11.sp,
                fontFamily = FontFamily.Monospace,
                color = Brand.Mut,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            Text(df.format(Date(h.time * 1000)), fontSize = 11.sp, color = Brand.Faint, fontFamily = Inter)
        }
        Spacer(Modifier.width(8.dp))
        Text(
            (if (incoming) "+" else "-") + fmtCrb(h.amount),
            fontFamily = FontFamily.Monospace,
            fontWeight = FontWeight.Bold,
            fontSize = 14.sp,
            color = accent,
        )
    }
}

// -------------------------------------------------------------------- settings

@Composable
private fun SettingsScreen(store: WalletStore, onReset: () -> Unit) {
    val ctx = LocalContext.current
    val activity = remember { ctx.findActivity() }
    val bioAvailable = remember { BiometricGate.available(ctx) }
    val accounts = remember { store.addresses() }

    var endpoint by remember { mutableStateOf(store.endpoint()) }
    var selected by remember { mutableStateOf(accounts.firstOrNull()?.addr ?: "") }
    var revealed by remember { mutableStateOf<String?>(null) }
    var checkingUpdate by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    // Re-seal the revealed key whenever the picked address changes.
    LaunchedEffect(selected) { revealed = null }

    ScreenColumn {
        ScreenTitle("Settings")
        Spacer(Modifier.height(16.dp))

        // ---- node endpoint
        GlassCard(modifier = Modifier.fillMaxWidth()) {
            BrandLabel("Node RPC endpoint")
            Spacer(Modifier.height(8.dp))
            BrandTextField(endpoint, { endpoint = it }, "https://…", Modifier.fillMaxWidth())
            Spacer(Modifier.height(12.dp))
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                SecondaryButton(text = "Lite default", modifier = Modifier.weight(1f)) {
                    endpoint = WalletStore.DEFAULT_ENDPOINT
                    store.setEndpoint(endpoint)
                    Toast.makeText(ctx, "Using lite default", Toast.LENGTH_SHORT).show()
                }
                GradientButton(text = "Save", modifier = Modifier.weight(1f)) {
                    store.setEndpoint(endpoint)
                    Toast.makeText(ctx, "Saved", Toast.LENGTH_SHORT).show()
                }
            }
        }

        // ---- app lock status
        Spacer(Modifier.height(16.dp))
        GlassCard(modifier = Modifier.fillMaxWidth()) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Filled.Fingerprint, contentDescription = null, tint = if (bioAvailable) Brand.Ok else Brand.Warn, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(8.dp))
                Text(
                    "App lock",
                    color = Brand.Txt,
                    fontFamily = SpaceGrotesk,
                    fontWeight = FontWeight.Bold,
                    fontSize = 16.sp,
                )
            }
            Spacer(Modifier.height(8.dp))
            Text(
                if (bioAvailable) {
                    "Biometric / device-credential lock is active. The app, every payment, and key reveal require authentication."
                } else {
                    "No screen lock is set, so biometric protection is unavailable. Set a PIN, pattern or fingerprint in Android settings to protect this wallet."
                },
                fontSize = 12.sp,
                color = if (bioAvailable) Brand.Mut else Brand.Warn,
                fontFamily = Inter,
            )
        }

        // ---- backup / export
        Spacer(Modifier.height(16.dp))
        GlassCard(modifier = Modifier.fillMaxWidth()) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Filled.Lock, contentDescription = null, tint = Brand.Cyan, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(8.dp))
                Text(
                    "Backup & export",
                    color = Brand.Txt,
                    fontFamily = SpaceGrotesk,
                    fontWeight = FontWeight.Bold,
                    fontSize = 16.sp,
                )
            }
            Spacer(Modifier.height(8.dp))
            Text(
                "Private keys are stored encrypted in the Android Keystore and never leave the device. Revealing one requires authentication.",
                fontSize = 12.sp,
                color = Brand.Mut,
                fontFamily = Inter,
            )
            Spacer(Modifier.height(12.dp))
            AddressPicker(
                accounts = accounts,
                selected = selected,
                allowAuto = false,
                balances = emptyMap(),
            ) { selected = it }
            Spacer(Modifier.height(12.dp))
            if (revealed == null) {
                SecondaryButton(
                    text = "Reveal private key",
                    modifier = Modifier.fillMaxWidth(),
                    icon = Icons.Filled.Visibility,
                ) {
                    BiometricGate.prompt(
                        activity,
                        title = "Reveal private key",
                        subtitle = "Authenticate to view your key",
                        onSuccess = { revealed = store.exportPrivateKey(selected) ?: "(none)" },
                        onCancel = {},
                    )
                }
            } else {
                Box(
                    Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(12.dp))
                        .background(Brand.Field)
                        .border(1.dp, Brand.Border2, RoundedCornerShape(12.dp))
                        .padding(14.dp),
                ) {
                    Text(
                        revealed ?: "(none)",
                        fontFamily = FontFamily.Monospace,
                        fontSize = 12.sp,
                        color = Brand.Cyan,
                    )
                }
                Spacer(Modifier.height(12.dp))
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    SecondaryButton(text = "Hide", modifier = Modifier.weight(1f), icon = Icons.Filled.VisibilityOff) {
                        revealed = null
                    }
                    GradientButton(
                        text = "Copy key",
                        modifier = Modifier.weight(1f),
                        icon = Icons.Filled.ContentCopy,
                    ) { copyPrivateKey(ctx, revealed ?: "") }
                }
            }
        }

        // ---- about / update
        Spacer(Modifier.height(16.dp))
        GlassCard(modifier = Modifier.fillMaxWidth()) {
            BrandLabel("About")
            Spacer(Modifier.height(8.dp))
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                Text("Cereblix Wallet", color = Brand.Txt, fontFamily = SpaceGrotesk, fontSize = 14.sp)
                Text("v$APP_VERSION", color = Brand.Mut, fontFamily = FontFamily.Monospace, fontSize = 13.sp)
            }
            Spacer(Modifier.height(12.dp))
            SecondaryButton(
                text = if (checkingUpdate) "Checking…" else "Check for updates",
                modifier = Modifier.fillMaxWidth(),
                icon = Icons.Filled.Refresh,
            ) {
                if (checkingUpdate) return@SecondaryButton
                checkingUpdate = true
                scope.launch {
                    val info = withContext(Dispatchers.IO) { UpdateChecker.check() }
                    checkingUpdate = false
                    if (info == null) {
                        Toast.makeText(ctx, "You're up to date", Toast.LENGTH_SHORT).show()
                    } else {
                        runCatching { ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(info.url))) }
                    }
                }
            }
        }

        // ---- danger zone
        Spacer(Modifier.height(16.dp))
        GlassCard(modifier = Modifier.fillMaxWidth()) {
            SecondaryButton(
                text = "Remove wallet from this device",
                modifier = Modifier.fillMaxWidth(),
                danger = true,
            ) { onReset() }
            Spacer(Modifier.height(8.dp))
            Text(
                "This deletes ALL addresses and their keys. Make sure every key is backed up first.",
                fontSize = 11.sp,
                color = Brand.Faint,
                fontFamily = Inter,
            )
        }
    }
}

// --------------------------------------------------------------------- update banner

@Composable
private fun UpdateBanner(info: UpdateInfo, onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    val shape = RoundedCornerShape(14.dp)
    Row(
        Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 10.dp)
            .clip(shape)
            .background(Brand.Glass2)
            .border(1.dp, Brand.Border2, shape)
            .padding(horizontal = 14.dp, vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(
                "Update available — v${info.version}",
                color = Brand.Cyan,
                fontFamily = SpaceGrotesk,
                fontWeight = FontWeight.Bold,
                fontSize = 14.sp,
            )
            if (info.notes.isNotBlank()) {
                Text(
                    info.notes,
                    color = Brand.Mut,
                    fontFamily = Inter,
                    fontSize = 12.sp,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
        Spacer(Modifier.width(10.dp))
        Box(
            Modifier
                .clip(RoundedCornerShape(10.dp))
                .background(Brand.gradient)
                .clickable { runCatching { ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(info.url))) } }
                .padding(horizontal = 14.dp, vertical = 8.dp),
        ) {
            Text("Update", color = Brand.Ink, fontFamily = SpaceGrotesk, fontWeight = FontWeight.Bold, fontSize = 13.sp)
        }
        Spacer(Modifier.width(6.dp))
        Icon(
            Icons.Filled.Close,
            contentDescription = "Dismiss",
            tint = Brand.Mut,
            modifier = Modifier.size(20.dp).clickable { onDismiss() },
        )
    }
}

// --------------------------------------------------------------------- brand bits

/** DNA-helix logo with a soft cyan glow. */
@Composable
private fun BrandLogo(logo: Dp = 30.dp) {
    Box(
        contentAlignment = Alignment.Center,
        modifier = Modifier
            .size(logo + 16.dp)
            .drawBehind {
                drawCircle(
                    brush = Brush.radialGradient(
                        colors = listOf(Color(0x5522D3EE), Color.Transparent),
                    ),
                )
            },
    ) {
        Image(
            painter = painterResource(R.drawable.logo),
            contentDescription = "Cereblix",
            modifier = Modifier.size(logo),
        )
    }
}

/** "Cereblix" wordmark — final syllable "lix" in brand violet. */
@Composable
private fun Wordmark(fontSize: TextUnit = 20.sp) {
    Row {
        Text(
            "Cereb",
            fontFamily = SpaceGrotesk,
            fontWeight = FontWeight.Bold,
            fontSize = fontSize,
            color = Brand.Txt,
            letterSpacing = 0.2.sp,
        )
        Text(
            "lix",
            fontFamily = SpaceGrotesk,
            fontWeight = FontWeight.Bold,
            fontSize = fontSize,
            color = Brand.Violet,
            letterSpacing = 0.2.sp,
        )
    }
}

@Composable
private fun BrandTopBar() {
    Column {
        Row(
            Modifier.fillMaxWidth().padding(horizontal = 20.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            BrandLogo(logo = 28.dp)
            Spacer(Modifier.width(8.dp))
            Wordmark(fontSize = 20.sp)
        }
        BrandDivider()
    }
}

// --------------------------------------------------------------------- shared

@Composable
private fun ScreenColumn(
    horizontalAlignment: Alignment.Horizontal = Alignment.Start,
    content: @Composable ColumnScope.() -> Unit,
) {
    Column(
        Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(start = 20.dp, end = 20.dp, top = 20.dp, bottom = 32.dp),
        horizontalAlignment = horizontalAlignment,
        content = content,
    )
}

@Composable
private fun ScreenTitle(text: String) {
    Text(
        text,
        fontFamily = SpaceGrotesk,
        fontWeight = FontWeight.Bold,
        fontSize = 24.sp,
        color = Brand.Txt,
        letterSpacing = (-0.4).sp,
    )
}

@Composable
private fun BrandLabel(text: String) {
    Text(
        text.uppercase(Locale.US),
        fontFamily = Inter,
        fontWeight = FontWeight.Medium,
        fontSize = 11.sp,
        color = Brand.Faint,
        letterSpacing = 1.2.sp,
    )
}

@Composable
private fun BrandDivider() {
    Box(Modifier.fillMaxWidth().height(1.dp).background(Brand.Border))
}

@Composable
private fun GlassCard(
    modifier: Modifier = Modifier,
    accentTop: Boolean = false,
    opaque: Boolean = false,
    padding: Dp = 18.dp,
    content: @Composable ColumnScope.() -> Unit,
) {
    val shape = RoundedCornerShape(18.dp)
    Column(
        modifier
            .clip(shape)
            // Floating overlays (dialogs) must be OPAQUE so the UI behind them does
            // not show through: a solid navy panel + a faint glass tint. In-flow
            // cards stay translucent (they sit on the aurora canvas).
            .then(if (opaque) Modifier.background(Brand.Panel).background(Brand.Glass) else Modifier.background(Brand.Glass2))
            .border(1.dp, Brand.Border, shape),
    ) {
        if (accentTop) {
            Box(Modifier.fillMaxWidth().height(2.dp).background(Brand.gradient))
        }
        Column(Modifier.padding(padding), content = content)
    }
}

/** Brand-styled address selector backed by a DropdownMenu. */
@Composable
private fun AddressPicker(
    accounts: List<WalletStore.Account>,
    selected: String,
    allowAuto: Boolean,
    balances: Map<String, Long>,
    modifier: Modifier = Modifier,
    onSelect: (String) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    val shape = RoundedCornerShape(13.dp)
    val current = accounts.firstOrNull { it.addr == selected }
    val label = when {
        selected.isEmpty() && allowAuto -> "Auto — pick a funded address"
        current != null -> current.label + " · " + shortMid(current.addr)
        else -> "Select address"
    }
    Box(modifier) {
        Row(
            Modifier
                .fillMaxWidth()
                .clip(shape)
                .background(Brand.Field)
                .border(1.dp, Brand.Border2, shape)
                .clickable { open = true }
                .padding(horizontal = 14.dp, vertical = 14.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                label,
                color = Brand.Txt,
                fontFamily = Inter,
                fontSize = 14.sp,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )
            Icon(Icons.Filled.ExpandMore, contentDescription = "Choose", tint = Brand.Mut, modifier = Modifier.size(20.dp))
        }
        DropdownMenu(
            expanded = open,
            onDismissRequest = { open = false },
            modifier = Modifier.background(Brand.Panel),
        ) {
            if (allowAuto) {
                DropdownMenuItem(
                    text = { Text("Auto — pick a funded address", color = Brand.Txt, fontFamily = Inter, fontSize = 14.sp) },
                    onClick = { onSelect(""); open = false },
                )
            }
            accounts.forEach { a ->
                val bal = balances[a.addr]
                DropdownMenuItem(
                    text = {
                        Column {
                            Text(a.label, color = Brand.Txt, fontFamily = SpaceGrotesk, fontSize = 14.sp)
                            Text(
                                shortMid(a.addr) + (bal?.let { "  ·  " + fmtCrb(it) + " CRB" } ?: ""),
                                color = Brand.Mut,
                                fontFamily = FontFamily.Monospace,
                                fontSize = 11.sp,
                            )
                        }
                    },
                    onClick = { onSelect(a.addr); open = false },
                )
            }
        }
    }
}

@Composable
private fun StatChip(k: String, v: String, modifier: Modifier = Modifier) {
    val shape = RoundedCornerShape(12.dp)
    Column(
        modifier
            .clip(shape)
            .background(Brand.Glass)
            .border(1.dp, Brand.Border, shape)
            .padding(horizontal = 14.dp, vertical = 12.dp),
    ) {
        Text(
            k.uppercase(Locale.US),
            fontFamily = Inter,
            fontSize = 10.sp,
            color = Brand.Faint,
            letterSpacing = 1.sp,
        )
        Spacer(Modifier.height(4.dp))
        Text(
            v,
            fontFamily = SpaceGrotesk,
            fontWeight = FontWeight.Medium,
            fontSize = 16.sp,
            color = Brand.Txt,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

@Composable
private fun GradientButton(
    text: String,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    loading: Boolean = false,
    icon: ImageVector? = null,
    onClick: () -> Unit,
) {
    val shape = RoundedCornerShape(14.dp)
    val fg = if (enabled) Brand.Ink else Brand.Mut
    val fill = if (enabled) Modifier.background(Brand.gradient) else Modifier.background(Brand.Glass2)
    val edge = if (enabled) Modifier.border(1.dp, Color.White.copy(alpha = 0.18f), shape)
        else Modifier.border(1.dp, Brand.Border, shape)
    Row(
        modifier
            .clip(shape)
            .then(fill)
            .then(edge)
            .clickable(enabled = enabled && !loading) { onClick() }
            .padding(vertical = 14.dp, horizontal = 20.dp),
        horizontalArrangement = Arrangement.Center,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (loading) {
            CircularProgressIndicator(
                color = Brand.Ink,
                strokeWidth = 2.dp,
                modifier = Modifier.size(20.dp),
            )
        } else {
            icon?.let {
                Icon(it, contentDescription = null, tint = fg, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(8.dp))
            }
            Text(
                text,
                color = fg,
                fontFamily = SpaceGrotesk,
                fontWeight = FontWeight.Bold,
                fontSize = 15.sp,
                letterSpacing = 0.2.sp,
            )
        }
    }
}

@Composable
private fun SecondaryButton(
    text: String,
    modifier: Modifier = Modifier,
    icon: ImageVector? = null,
    danger: Boolean = false,
    enabled: Boolean = true,
    onClick: () -> Unit,
) {
    val shape = RoundedCornerShape(14.dp)
    val fg = if (danger) Brand.Bad else Brand.Txt
    val bd = if (danger) Brand.Bad.copy(alpha = 0.40f) else Brand.Border2
    val bg = if (danger) Brand.Bad.copy(alpha = 0.10f) else Brand.Glass2
    Row(
        modifier
            .clip(shape)
            .background(bg)
            .border(1.dp, bd, shape)
            .clickable(enabled = enabled) { onClick() }
            .padding(vertical = 12.dp, horizontal = 18.dp),
        horizontalArrangement = Arrangement.Center,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        icon?.let {
            Icon(it, contentDescription = null, tint = fg, modifier = Modifier.size(18.dp))
            Spacer(Modifier.width(8.dp))
        }
        Text(
            text,
            color = fg,
            fontFamily = SpaceGrotesk,
            fontWeight = FontWeight.Medium,
            fontSize = 15.sp,
        )
    }
}

@Composable
private fun CopyIconButton(onClick: () -> Unit) {
    val shape = RoundedCornerShape(10.dp)
    Box(
        Modifier
            .size(38.dp)
            .clip(shape)
            .background(Brand.Glass)
            .border(1.dp, Brand.Border, shape)
            .clickable { onClick() },
        contentAlignment = Alignment.Center,
    ) {
        Icon(Icons.Filled.ContentCopy, contentDescription = "Copy", tint = Brand.Cyan, modifier = Modifier.size(18.dp))
    }
}

@Composable
private fun BrandTextField(
    value: String,
    onValueChange: (String) -> Unit,
    label: String,
    modifier: Modifier = Modifier,
    singleLine: Boolean = true,
    keyboardOptions: KeyboardOptions = KeyboardOptions.Default,
    visualTransformation: VisualTransformation = VisualTransformation.None,
    trailingIcon: @Composable (() -> Unit)? = null,
) {
    OutlinedTextField(
        value = value,
        onValueChange = onValueChange,
        label = { Text(label) },
        singleLine = singleLine,
        keyboardOptions = keyboardOptions,
        visualTransformation = visualTransformation,
        trailingIcon = trailingIcon,
        shape = RoundedCornerShape(13.dp),
        modifier = modifier,
        colors = OutlinedTextFieldDefaults.colors(
            focusedBorderColor = Brand.Cyan,
            unfocusedBorderColor = Brand.Border2,
            focusedContainerColor = Brand.Field,
            unfocusedContainerColor = Brand.Field,
            cursorColor = Brand.Cyan,
            focusedLabelColor = Brand.Cyan,
            unfocusedLabelColor = Brand.Mut,
            focusedTextColor = Brand.Txt,
            unfocusedTextColor = Brand.Txt,
            focusedTrailingIconColor = Brand.Mut,
            unfocusedTrailingIconColor = Brand.Mut,
        ),
    )
}

@Composable
private fun ErrorBanner(msg: String) {
    Spacer(Modifier.height(12.dp))
    val shape = RoundedCornerShape(10.dp)
    Box(
        Modifier
            .fillMaxWidth()
            .clip(shape)
            .background(Brand.Bad.copy(alpha = 0.10f))
            .border(1.dp, Brand.Bad.copy(alpha = 0.30f), shape)
            .padding(12.dp),
    ) {
        Text(msg, color = Color(0xFFFFC4C7), fontSize = 13.sp, fontFamily = Inter)
    }
}

/** Aurora canvas: deep navy with cyan (top-left) + violet (top-right) radial glows. */
@Composable
private fun AuroraBackground(content: @Composable () -> Unit) {
    Box(
        Modifier
            .fillMaxSize()
            .background(Brand.Bg)
            .drawBehind {
                drawRect(
                    Brush.radialGradient(
                        colors = listOf(Color(0x4D22D3EE), Color.Transparent),
                        center = Offset(size.width * 0.10f, 0f),
                        radius = size.width * 0.95f,
                    ),
                )
                drawRect(
                    Brush.radialGradient(
                        colors = listOf(Color(0x42A78BFA), Color.Transparent),
                        center = Offset(size.width * 0.92f, size.height * 0.02f),
                        radius = size.width * 0.95f,
                    ),
                )
                drawRect(
                    Brush.radialGradient(
                        colors = listOf(Color(0x1A38BDF8), Color.Transparent),
                        center = Offset(size.width * 0.55f, size.height * 0.42f),
                        radius = size.width * 0.85f,
                    ),
                )
            },
    ) {
        content()
    }
}

// --------------------------------------------------------------------- clipboard

private fun copyToClipboard(ctx: Context, text: String) {
    val cm = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
    cm.setPrimaryClip(ClipData.newPlainText("cereblix", text))
    Toast.makeText(ctx, "Copied", Toast.LENGTH_SHORT).show()
}

/**
 * Copy the PRIVATE KEY: flag the clip sensitive (API 33+, keeps it out of clipboard
 * previews / history) and auto-clear it after ~30s so it doesn't linger.
 */
private fun copyPrivateKey(ctx: Context, text: String) {
    if (text.isEmpty()) return
    val cm = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
    val clip = ClipData.newPlainText("cereblix-key", text)
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
        clip.description.extras = PersistableBundle().apply {
            putBoolean(ClipDescription.EXTRA_IS_SENSITIVE, true)
        }
    }
    cm.setPrimaryClip(clip)
    Toast.makeText(ctx, "Key copied — clears in 30s", Toast.LENGTH_SHORT).show()

    Handler(Looper.getMainLooper()).postDelayed({
        val current = cm.primaryClip
        val stillOurs = current != null && current.itemCount > 0 &&
            current.getItemAt(0).text?.toString() == text
        if (stillOurs) {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                cm.clearPrimaryClip()
            } else {
                cm.setPrimaryClip(ClipData.newPlainText("", ""))
            }
        }
    }, 30_000)
}
