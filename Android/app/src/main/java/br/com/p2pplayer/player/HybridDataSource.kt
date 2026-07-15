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
 * DataSource híbrido: store → P2P → HTTP.
 *
 * Live com N peers: seeder **por segmento** (hash do id % N) HTTP-baixa e anuncia;
 * os outros esperam/prefetch via P2P. Com 2 peers ≈ 50% HTTP e 50% P2P em cada lado
 * (compartilhamento bidirecional).
 */
class HybridDataSource(
    private val httpClient: OkHttpClient,
    private val store: SegmentStore,
    private val trafficStats: TrafficStats,
    private val maxParallelP2p: AtomicInteger = AtomicInteger(3),
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
            if (data == null && mesh != null && mesh.hasSignaledPeers()) {
                val iSeed = mesh.isSegmentSeeder(segmentId)
                // Quem não é dono do segmento espera o seeder anunciar.
                if (!mesh.peerHasPublic(segmentId) && !iSeed) {
                    mesh.awaitPeerSegment(segmentId, timeoutMs = 2_800)
                }
                data = store.get(segmentId)
                if (data == null && mesh.peerHasPublic(segmentId) && maxParallelP2p.decrementAndGet() >= 0) {
                    try {
                        data = mesh.requestSegment(segmentId, timeoutMs = 8_000)
                        fromP2p = data != null
                    } finally {
                        maxParallelP2p.incrementAndGet()
                    }
                } else if (data != null && !iSeed) {
                    fromP2p = true
                } else if (!mesh.peerHasPublic(segmentId) && !iSeed) {
                    android.util.Log.d(
                        "HybridDS",
                        "P2P skip seed=${iSeed} id=${segmentId.take(8)} → HTTP",
                    )
                }
            }
        }

        if (data == null) {
            data = fetchHttp(url)
            if (segmentId != null) {
                store.putKeyed(segmentId, data)
                PeerMeshHolder.current.get()?.announceHave(segmentId)
            }
        } else if (fromP2p) {
            android.util.Log.d("HybridDS", "segment via P2P id=${segmentId?.take(8)} size=${data.size}")
            if (segmentId != null) {
                PeerMeshHolder.current.get()?.announceHave(segmentId)
            }
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
        private val parallel = AtomicInteger(3)
        override fun createDataSource(): HybridDataSource =
            HybridDataSource(httpClient, store, trafficStats, parallel)
    }
}
