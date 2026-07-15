package br.com.p2pplayer.player

import android.net.Uri
import androidx.media3.common.C
import androidx.media3.datasource.BaseDataSource
import androidx.media3.datasource.DataSpec
import br.com.p2pplayer.net.TrafficStats
import br.com.p2pplayer.p2p.PeerMeshHolder
import br.com.p2pplayer.p2p.SegmentStore
import okhttp3.OkHttpClient
import okhttp3.Request
import java.io.ByteArrayInputStream
import java.io.IOException
import java.io.InputStream
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

/**
 * DataSource híbrido: segmentos /u/{urlKey} tentam P2P primeiro; fallback HTTP.
 */
class HybridDataSource(
    private val httpClient: OkHttpClient,
    private val store: SegmentStore,
    private val trafficStats: TrafficStats,
    private val maxParallelP2p: AtomicInteger = AtomicInteger(2),
) : BaseDataSource(/* isNetwork= */ true) {

    private var input: InputStream? = null
    private var bytesRemaining: Long = 0
    private var opened = false
    private var uri: Uri? = null

    override fun open(dataSpec: DataSpec): Long {
        uri = dataSpec.uri
        val url = dataSpec.uri.toString()
        val segmentId = extractSegmentId(url)
        var data: ByteArray? = null
        var fromP2p = false

        if (segmentId != null) {
            data = store.get(segmentId)
            val mesh = PeerMeshHolder.current.get()
            // Live: os 2 peers pedem o mesmo live edge. Um precisa HTTP-seed e anunciar;
            // o outro espera o inventário e puxa via relay/WS.
            if (data == null && mesh != null && mesh.hasSignaledPeers()) {
                if (!mesh.peerHasPublic(segmentId)) {
                    // Jitter evita os dois caírem no HTTP ao mesmo tempo.
                    try {
                        Thread.sleep((150L..700L).random())
                    } catch (_: InterruptedException) {
                    }
                    mesh.awaitPeerSegment(segmentId, timeoutMs = 2500)
                }
                if (mesh.peerHasPublic(segmentId) && maxParallelP2p.decrementAndGet() >= 0) {
                    try {
                        data = mesh.requestSegment(segmentId, timeoutMs = 6000)
                        fromP2p = data != null
                    } finally {
                        maxParallelP2p.incrementAndGet()
                    }
                } else if (!mesh.peerHasPublic(segmentId)) {
                    android.util.Log.d("HybridDS", "P2P skip (peer sem id=${segmentId.take(8)}) → HTTP")
                }
            }
        }

        if (data == null) {
            data = fetchHttp(url)
            if (segmentId != null) {
                store.putKeyed(segmentId, data)
                PeerMeshHolder.current.get()?.announceInventory()
            }
        } else if (fromP2p) {
            android.util.Log.d("HybridDS", "segment via P2P id=${segmentId?.take(8)} size=${data.size}")
        }

        val offset = dataSpec.position.toInt().coerceAtLeast(0)
        val slice = if (offset > 0) data.copyOfRange(offset, data.size) else data
        input = ByteArrayInputStream(slice)
        bytesRemaining = if (dataSpec.length != C.LENGTH_UNSET.toLong()) {
            dataSpec.length
        } else {
            slice.size.toLong()
        }
        opened = true
        transferStarted(dataSpec)
        return bytesRemaining
    }

    override fun read(buffer: ByteArray, offset: Int, length: Int): Int {
        if (length == 0) return 0
        if (bytesRemaining == 0L) return C.RESULT_END_OF_INPUT
        val toRead = if (bytesRemaining == C.LENGTH_UNSET.toLong()) length
        else minOf(length.toLong(), bytesRemaining).toInt()
        val read = input!!.read(buffer, offset, toRead)
        if (read == -1) {
            return C.RESULT_END_OF_INPUT
        }
        if (bytesRemaining != C.LENGTH_UNSET.toLong()) {
            bytesRemaining -= read
        }
        bytesTransferred(read)
        return read
    }

    override fun getUri(): Uri? = uri

    override fun close() {
        if (opened) {
            opened = false
            transferEnded()
        }
        input?.close()
        input = null
    }

    private fun fetchHttp(url: String): ByteArray {
        val req = Request.Builder().url(url).get().build()
        httpClient.newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) {
                throw IOException("HTTP ${resp.code} for $url")
            }
            val bytes = resp.body?.bytes() ?: ByteArray(0)
            trafficStats.addHttpDown(bytes.size.toLong())
            android.util.Log.d("HybridDS", "HTTP ↓ ${bytes.size}B url=${url.takeLast(48)}")
            return bytes
        }
    }

    companion object {
        private val SEG_REGEX = Regex("""/(?:u|seg)/([a-fA-F0-9]{64})(?:\.ts)?(?:\?|$)""")

        fun extractSegmentId(url: String): String? =
            SEG_REGEX.find(url)?.groupValues?.getOrNull(1)?.lowercase()

        fun defaultHttpClient(): OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(20, TimeUnit.SECONDS)
            .followRedirects(true)
            .build()
    }

    class Factory(
        private val store: SegmentStore,
        private val trafficStats: TrafficStats,
        private val httpClient: OkHttpClient = defaultHttpClient(),
    ) : androidx.media3.datasource.DataSource.Factory {
        private val parallel = AtomicInteger(2)
        override fun createDataSource(): HybridDataSource =
            HybridDataSource(httpClient, store, trafficStats, parallel)
    }
}
