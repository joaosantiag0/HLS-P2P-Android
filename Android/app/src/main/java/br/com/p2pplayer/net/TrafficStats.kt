package br.com.p2pplayer.net

import java.util.concurrent.atomic.AtomicLong

/**
 * Contadores centralizados de tráfego HTTP/P2P.
 * HTTP ↑ = sinalização/controle; HTTP ↓ = manifests/segmentos HTTP;
 * P2P ↓/↑ = bytes no DataChannel.
 */
class TrafficStats {
    private val httpDown = AtomicLong(0)
    private val httpUp = AtomicLong(0)
    private val p2pDown = AtomicLong(0)
    private val p2pUp = AtomicLong(0)

    @Volatile private var httpDownWindow = 0L
    @Volatile private var httpUpWindow = 0L
    @Volatile private var p2pDownWindow = 0L
    @Volatile private var p2pUpWindow = 0L
    @Volatile private var lastTickMs = System.currentTimeMillis()
    @Volatile private var httpDownRate = 0.0
    @Volatile private var httpUpRate = 0.0
    @Volatile private var p2pDownRate = 0.0
    @Volatile private var p2pUpRate = 0.0
    @Volatile var peerCount: Int = 0
    @Volatile var signaledPeers: Int = 0
    @Volatile var connectedPeers: Int = 0

    fun addHttpDown(bytes: Long) {
        if (bytes <= 0) return
        httpDown.addAndGet(bytes)
        synchronized(this) { httpDownWindow += bytes }
    }

    fun addHttpUp(bytes: Long) {
        if (bytes <= 0) return
        httpUp.addAndGet(bytes)
        synchronized(this) { httpUpWindow += bytes }
    }

    fun addP2pDown(bytes: Long) {
        if (bytes <= 0) return
        p2pDown.addAndGet(bytes)
        synchronized(this) { p2pDownWindow += bytes }
    }

    fun addP2pUp(bytes: Long) {
        if (bytes <= 0) return
        p2pUp.addAndGet(bytes)
        synchronized(this) { p2pUpWindow += bytes }
    }

    fun tickRates() {
        val now = System.currentTimeMillis()
        val elapsed = (now - lastTickMs).coerceAtLeast(1)
        val sec = elapsed / 1000.0
        synchronized(this) {
            httpDownRate = httpDownWindow / sec
            httpUpRate = httpUpWindow / sec
            p2pDownRate = p2pDownWindow / sec
            p2pUpRate = p2pUpWindow / sec
            httpDownWindow = 0
            httpUpWindow = 0
            p2pDownWindow = 0
            p2pUpWindow = 0
            lastTickMs = now
        }
    }

    fun snapshot(): Snapshot = Snapshot(
        httpDownTotal = httpDown.get(),
        httpUpTotal = httpUp.get(),
        p2pDownTotal = p2pDown.get(),
        p2pUpTotal = p2pUp.get(),
        httpDownRate = httpDownRate,
        httpUpRate = httpUpRate,
        p2pDownRate = p2pDownRate,
        p2pUpRate = p2pUpRate,
        peerCount = peerCount,
        signaledPeers = signaledPeers,
        connectedPeers = connectedPeers,
    )

    data class Snapshot(
        val httpDownTotal: Long,
        val httpUpTotal: Long,
        val p2pDownTotal: Long,
        val p2pUpTotal: Long,
        val httpDownRate: Double,
        val httpUpRate: Double,
        val p2pDownRate: Double,
        val p2pUpRate: Double,
        val peerCount: Int,
        val signaledPeers: Int,
        val connectedPeers: Int,
    )
}

fun formatBytes(n: Long): String {
    if (n < 1024) return "${n}B"
    val kb = n / 1024.0
    if (kb < 1024) return String.format("%.1fKB", kb)
    val mb = kb / 1024.0
    if (mb < 1024) return String.format("%.2fMB", mb)
    return String.format("%.2fGB", mb / 1024.0)
}

fun formatRate(bps: Double): String {
    if (bps < 1024) return String.format("%.0fB/s", bps)
    val kb = bps / 1024.0
    if (kb < 1024) return String.format("%.1fKB/s", kb)
    return String.format("%.2fMB/s", kb / 1024.0)
}
