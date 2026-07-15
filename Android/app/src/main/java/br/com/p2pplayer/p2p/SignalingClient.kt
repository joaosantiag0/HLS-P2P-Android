package br.com.p2pplayer.p2p

import android.util.Log
import br.com.p2pplayer.net.TrafficStats
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

interface SignalingListener {
    fun onPeers(peerIds: List<String>)
    fun onPeerJoined(peerId: String)
    fun onPeerLeft(peerId: String)
    fun onSignal(from: String, sdpType: String?, sdp: String?, candidateJson: String?)
    fun onInventory(from: String, segmentIds: List<String>)
    fun onRelayWant(from: String, segmentId: String)
    fun onRelayChunk(from: String, segmentId: String, offset: Int, total: Int, dataB64: String)
    fun onRelayDone(from: String, segmentId: String)
    fun onRelayNack(from: String, segmentId: String)
    fun onClosed()
}

class SignalingClient(
    private val wsUrl: String,
    private val peerId: String,
    private val streamKey: String,
    private val sessionId: String,
    private val trafficStats: TrafficStats,
    private val accessSecret: String? = null,
) {
    private val tag = "Signaling"
    private val client = OkHttpClient.Builder()
        .pingInterval(15, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS)
        .build()
    private var socket: WebSocket? = null
    private val listeners = CopyOnWriteArrayList<SignalingListener>()
    // Nunca processar mensagens (nem enviar relay) na thread do OkHttp WebSocket.
    private val dispatch = Executors.newSingleThreadExecutor()
    private val relaySend = Executors.newSingleThreadExecutor()
    private val stopped = AtomicBoolean(false)
    private val connecting = AtomicBoolean(false)
    private val generation = java.util.concurrent.atomic.AtomicInteger(0)

    fun addListener(l: SignalingListener) {
        listeners.add(l)
    }

    fun connect() {
        if (stopped.get()) return
        if (!connecting.compareAndSet(false, true)) return
        val url = if (!accessSecret.isNullOrBlank() && !wsUrl.contains("access=")) {
            if (wsUrl.contains("?")) "$wsUrl&access=$accessSecret" else "$wsUrl?access=$accessSecret"
        } else wsUrl
        Log.i(tag, "connecting $url streamKey=${streamKey.take(24)}…")
        val gen = generation.incrementAndGet()
        socket?.cancel()
        socket = null
        val req = Request.Builder().url(url).build()
        socket = client.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                if (gen != generation.get()) return
                connecting.set(false)
                Log.i(tag, "open")
                val hello = JSONObject()
                    .put("type", "hello")
                    .put("peerId", peerId)
                    .put("streamKey", streamKey)
                    .put("sessionId", sessionId)
                sendRaw(hello.toString())
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                if (gen != generation.get()) return
                // Despacha fora da thread do WS — crítico para relay não travar/fechar o socket.
                dispatch.execute { handleMessage(text) }
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                if (gen != generation.get()) return
                connecting.set(false)
                Log.w(tag, "closed code=$code reason=$reason")
                dispatch.execute {
                    listeners.forEach { it.onClosed() }
                    scheduleReconnect()
                }
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                if (gen != generation.get()) return
                connecting.set(false)
                Log.e(tag, "failure: ${t.message}", t)
                dispatch.execute {
                    listeners.forEach { it.onClosed() }
                    scheduleReconnect()
                }
            }
        })
    }

    private fun scheduleReconnect() {
        if (stopped.get()) return
        dispatch.execute {
            try {
                Thread.sleep(1500)
            } catch (_: InterruptedException) {
                return@execute
            }
            if (!stopped.get()) {
                Log.i(tag, "reconnecting…")
                connect()
            }
        }
    }

    fun sendSignal(to: String, sdpType: String?, sdp: String?, candidateJson: String?) {
        val o = JSONObject()
            .put("type", "signal")
            .put("to", to)
            .put("from", peerId)
        if (sdpType != null) o.put("sdpType", sdpType)
        if (sdp != null) o.put("sdp", sdp)
        if (candidateJson != null) o.put("candidate", JSONObject(candidateJson))
        sendRaw(o.toString())
    }

    fun sendInventory(ids: Collection<String>) {
        val trimmed = ids.toList().take(40)
        val arr = JSONArray()
        trimmed.forEach { arr.put(it) }
        val o = JSONObject()
            .put("type", "inventory")
            .put("segmentIds", arr)
        sendRaw(o.toString())
    }

    fun sendRelayWant(to: String, segmentId: String) {
        sendRaw(
            JSONObject()
                .put("type", "p2p-want")
                .put("to", to)
                .put("from", peerId)
                .put("id", segmentId)
                .toString(),
            countHttp = false,
        )
    }

    fun sendRelayChunk(to: String, segmentId: String, offset: Int, total: Int, dataB64: String) {
        sendRaw(
            JSONObject()
                .put("type", "p2p-chunk")
                .put("to", to)
                .put("from", peerId)
                .put("id", segmentId)
                .put("offset", offset)
                .put("total", total)
                .put("data", dataB64)
                .toString(),
            countHttp = false,
        )
    }

    fun sendRelayDone(to: String, segmentId: String) {
        sendRaw(
            JSONObject()
                .put("type", "p2p-done")
                .put("to", to)
                .put("from", peerId)
                .put("id", segmentId)
                .toString(),
            countHttp = false,
        )
    }

    fun sendRelayNack(to: String, segmentId: String, reason: String) {
        sendRaw(
            JSONObject()
                .put("type", "p2p-nack")
                .put("to", to)
                .put("from", peerId)
                .put("id", segmentId)
                .put("reason", reason)
                .toString(),
            countHttp = false,
        )
    }

    /** Envia resposta de relay em background (não bloquear dispatch/WS). */
    fun sendRelayResponse(block: () -> Unit) {
        relaySend.execute {
            try {
                block()
            } catch (e: Exception) {
                Log.e(tag, "relay send error", e)
            }
        }
    }

    fun close() {
        stopped.set(true)
        socket?.close(1000, "bye")
        socket = null
        dispatch.shutdownNow()
        relaySend.shutdownNow()
    }

    private fun sendRaw(text: String, countHttp: Boolean = true) {
        if (countHttp) {
            trafficStats.addHttpUp(text.toByteArray().size.toLong())
        }
        val ok = socket?.send(text) ?: false
        if (!ok) {
            Log.w(tag, "send failed type=${text.take(40)} len=${text.length}")
        }
    }

    private fun handleMessage(text: String) {
        val json = try {
            JSONObject(text)
        } catch (e: Exception) {
            Log.e(tag, "bad json", e)
            return
        }
        val type = json.optString("type")
        if (!type.startsWith("p2p-")) {
            trafficStats.addHttpDown(text.toByteArray().size.toLong())
        }
        when (type) {
            "peers" -> {
                val arr = json.optJSONArray("peers") ?: JSONArray()
                val ids = mutableListOf<String>()
                for (i in 0 until arr.length()) {
                    ids.add(arr.getJSONObject(i).getString("peerId"))
                }
                Log.i(tag, "peers list size=${ids.size}")
                listeners.forEach { it.onPeers(ids) }
            }
            "peer-joined" -> {
                val id = json.getString("peerId")
                Log.i(tag, "peer-joined ${id.take(8)}")
                listeners.forEach { it.onPeerJoined(id) }
            }
            "peer-left" -> {
                val id = json.getString("peerId")
                Log.i(tag, "peer-left ${id.take(8)}")
                listeners.forEach { it.onPeerLeft(id) }
            }
            "signal" -> listeners.forEach {
                it.onSignal(
                    from = json.getString("from"),
                    sdpType = if (json.has("sdpType")) json.getString("sdpType") else null,
                    sdp = if (json.has("sdp")) json.getString("sdp") else null,
                    candidateJson = json.optJSONObject("candidate")?.toString(),
                )
            }
            "inventory" -> {
                val arr = json.optJSONArray("segmentIds") ?: JSONArray()
                val ids = mutableListOf<String>()
                for (i in 0 until arr.length()) ids.add(arr.getString(i))
                val from = if (json.has("from")) json.getString("from") else ""
                listeners.forEach { it.onInventory(from, ids) }
            }
            "p2p-want" -> listeners.forEach {
                it.onRelayWant(json.optString("from"), json.optString("id"))
            }
            "p2p-chunk" -> {
                val from = json.optString("from")
                val id = json.optString("id")
                val offset = json.optInt("offset", 0)
                val total = json.optInt("total", 0)
                val data = json.optString("data", "")
                if (id.isEmpty() || data.isEmpty()) {
                    Log.w(tag, "p2p-chunk incomplete id=${id.take(8)} dataLen=${data.length}")
                    return
                }
                listeners.forEach { it.onRelayChunk(from, id, offset, total, data) }
            }
            "p2p-done" -> listeners.forEach {
                it.onRelayDone(json.optString("from"), json.optString("id"))
            }
            "p2p-nack" -> listeners.forEach {
                it.onRelayNack(json.optString("from"), json.optString("id"))
            }
        }
    }
}
