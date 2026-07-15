package br.com.p2pplayer.p2p

import android.content.Context
import android.util.Base64
import android.util.Log
import br.com.p2pplayer.net.IceServerConfig
import br.com.p2pplayer.net.TrafficStats
import org.json.JSONArray
import org.json.JSONObject
import org.webrtc.DataChannel
import org.webrtc.IceCandidate
import org.webrtc.MediaConstraints
import org.webrtc.PeerConnection
import org.webrtc.PeerConnectionFactory
import org.webrtc.SessionDescription
import java.nio.ByteBuffer
import java.nio.charset.StandardCharsets
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

/**
 * WebRTC DataChannel mesh (max 3 peers) for segment exchange.
 */
class PeerMesh(
    context: Context,
    private val localPeerId: String,
    private val iceServers: List<IceServerConfig>,
    private val signaling: SignalingClient,
    private val store: SegmentStore,
    private val trafficStats: TrafficStats,
    private val maxPeers: Int = 3,
) : SignalingListener {

    private val tag = "PeerMesh"
    private val executor = Executors.newSingleThreadExecutor()
    private val peers = ConcurrentHashMap<String, PeerSlot>()
    private val remoteInventory = ConcurrentHashMap<String, MutableSet<String>>()
    private val pending = ConcurrentHashMap<String, PendingDownload>()
    private val listeners = CopyOnWriteArrayList<(String) -> Unit>()
    private val signaledPeerIds = ConcurrentHashMap.newKeySet<String>()
    private val inventoryLock = Object()
    @Volatile private var lastFullAnnounceAt = 0L
    private val prefetchExecutor = Executors.newFixedThreadPool(3)
    private val iceRetries = ConcurrentHashMap<String, Int>()

    private val factory: PeerConnectionFactory = WebRtcFactory.get(context)

    init {
        signaling.addListener(this)
    }

    fun addSegmentAvailableListener(l: (String) -> Unit) {
        listeners.add(l)
    }

    fun start() {
        signaling.connect()
    }

    fun hasSignaledPeers(): Boolean = signaledPeerIds.isNotEmpty()

    fun peerHasPublic(segmentId: String): Boolean = peerHas(segmentId)

    /**
     * Seeder por segmento: espalha HTTP no swarm para maximizar P2P bidirecional.
     * Com 2 peers, cada um HTTP-seeda ~50% dos segmentos e baixa o resto via P2P.
     */
    fun isSegmentSeeder(segmentId: String): Boolean {
        val swarm = swarmPeerIds()
        if (swarm.size <= 1) return true
        val sorted = swarm.sorted()
        val idx = segmentOwnerIndex(segmentId, sorted.size)
        return sorted[idx] == localPeerId
    }

    private fun swarmPeerIds(): Set<String> {
        val others = if (signaledPeerIds.isNotEmpty()) signaledPeerIds else remoteInventory.keys
        return others + localPeerId
    }

    private fun segmentOwnerIndex(segmentId: String, n: Int): Int {
        if (n <= 1 || segmentId.isEmpty()) return 0
        var h = 0
        for (c in segmentId) {
            h = (h * 31 + c.code) and 0x7fffffff
        }
        return h % n
    }

    fun awaitPeerSegment(segmentId: String, timeoutMs: Long) {
        if (peerHas(segmentId) || store.has(segmentId)) return
        val deadline = System.currentTimeMillis() + timeoutMs
        synchronized(inventoryLock) {
            while (!peerHas(segmentId) && !store.has(segmentId)) {
                val left = deadline - System.currentTimeMillis()
                if (left <= 0) return
                try {
                    inventoryLock.wait(left.coerceAtMost(200))
                } catch (_: InterruptedException) {
                    return
                }
            }
        }
    }

    private fun peerHas(segmentId: String): Boolean =
        remoteInventory.values.any { it.contains(segmentId) }

    /** Anúncio rápido de 1 segmento (crítico no live para o leecher não cair no HTTP). */
    fun announceHave(segmentId: String) {
        if (segmentId.isEmpty()) return
        signaling.sendInventory(listOf(segmentId))
        val msg = JSONObject().put("t", "have").put("ids", JSONArray().put(segmentId))
        peers.values.forEach { sendJson(it, msg) }
        // Full inventory debounced — evita flood no WS
        val now = System.currentTimeMillis()
        if (now - lastFullAnnounceAt >= 5_000L) {
            lastFullAnnounceAt = now
            announceInventory()
        }
    }

    fun announceInventory() {
        val ids = store.recentIds(40)
        if (ids.isEmpty() && signaledPeerIds.isEmpty()) return
        lastFullAnnounceAt = System.currentTimeMillis()
        signaling.sendInventory(ids)
        val arr = JSONArray()
        ids.forEach { arr.put(it) }
        val msg = JSONObject().put("t", "have").put("ids", arr)
        peers.values.forEach { sendJson(it, msg) }
        refreshPeerCount()
        Log.d(tag, "announce inventory size=${ids.size} peers=${signaledPeerIds.size}")
    }

    /**
     * Fetch segment via WebRTC DataChannel only (P2P real). Sem relay no servidor.
     */
    fun requestSegment(segmentId: String, timeoutMs: Long = 2500): ByteArray? {
        if (store.has(segmentId)) return store.get(segmentId)

        awaitOpenChannel(1_200)

        val dcPeer = peers.entries.firstOrNull { (peerId, slot) ->
            slot.channel?.state() == DataChannel.State.OPEN &&
                remoteInventory[peerId]?.contains(segmentId) == true
        }
        if (dcPeer == null) {
            Log.d(tag, "P2P miss (sem DC com segmento) id=${segmentId.take(8)}")
            return null
        }

        val pendingDownload = PendingDownload()
        pending[segmentId] = pendingDownload
        try {
            sendJson(dcPeer.value, JSONObject().put("t", "want").put("id", segmentId).put("offset", 0))
            if (!pendingDownload.waitDone(timeoutMs)) {
                Log.d(tag, "DC timeout $segmentId")
                return null
            }
            val data = pendingDownload.data
            if (data == null || data.isEmpty()) return null
            store.putKeyed(segmentId, data)
            Log.d(tag, "DC OK $segmentId size=${data.size}")
            listeners.forEach { it(segmentId) }
            return data
        } finally {
            pending.remove(segmentId)
        }
    }

    fun hasOpenDataChannel(): Boolean =
        peers.values.any { it.channel?.state() == DataChannel.State.OPEN }

    private fun awaitOpenChannel(timeoutMs: Long) {
        if (hasOpenDataChannel()) return
        val deadline = System.currentTimeMillis() + timeoutMs
        while (!hasOpenDataChannel() && System.currentTimeMillis() < deadline) {
            try {
                Thread.sleep(50)
            } catch (_: InterruptedException) {
                return
            }
        }
    }

    fun stop() {
        executor.execute {
            peers.values.forEach { it.close() }
            peers.clear()
            signaling.close()
        }
        executor.shutdown()
        prefetchExecutor.shutdownNow()
    }

    override fun onPeers(peerIds: List<String>) {
        signaledPeerIds.clear()
        signaledPeerIds.addAll(peerIds)
        peerIds.take(maxPeers).forEach { id ->
            if (localPeerId < id) connectAsOfferer(id)
        }
        refreshPeerCount()
        announceInventory()
    }

    override fun onPeerJoined(peerId: String) {
        signaledPeerIds.add(peerId)
        if (peers.size >= maxPeers) {
            refreshPeerCount()
            return
        }
        if (localPeerId < peerId) {
            connectAsOfferer(peerId)
        }
        refreshPeerCount()
        announceInventory()
    }

    override fun onPeerLeft(peerId: String) {
        signaledPeerIds.remove(peerId)
        peers.remove(peerId)?.close()
        remoteInventory.remove(peerId)
        refreshPeerCount()
    }

    override fun onSignal(from: String, sdpType: String?, sdp: String?, candidateJson: String?) {
        executor.execute {
            when (sdpType) {
                "offer" -> handleOffer(from, sdp)
                "answer" -> handleAnswer(from, sdp)
                "candidate" -> handleCandidate(from, candidateJson)
                else -> if (candidateJson != null) handleCandidate(from, candidateJson)
            }
        }
    }

    override fun onInventory(from: String, segmentIds: List<String>) {
        val set = remoteInventory.getOrPut(from) { ConcurrentHashMap.newKeySet() }
        // Lista grande = snapshot completo (substitui). Lista curta = delta "have".
        if (segmentIds.size >= 10) {
            set.clear()
        }
        set.addAll(segmentIds)
        // Evita crescer sem limite se só chegarem deltas
        if (set.size > 200 && segmentIds.isNotEmpty()) {
            set.clear()
            set.addAll(segmentIds)
        }
        Log.d(tag, "inventory from ${from.take(8)}: +${segmentIds.size} total=${set.size}")
        synchronized(inventoryLock) { inventoryLock.notifyAll() }
        refreshPeerCount()
        // Prefetch em deltas "have": quem não é seeder daquele segmento baixa via P2P.
        if (segmentIds.size in 1..3) {
            for (id in segmentIds) {
                if (store.has(id) || pending.containsKey(id)) continue
                if (isSegmentSeeder(id)) continue
                prefetchExecutor.execute {
                    try {
                        val data = requestSegment(id, timeoutMs = 8_000)
                        if (data != null) {
                            Log.d(tag, "prefetch OK ${id.take(8)} size=${data.size}")
                            synchronized(inventoryLock) { inventoryLock.notifyAll() }
                        }
                    } catch (e: Exception) {
                        Log.w(tag, "prefetch fail ${id.take(8)}: ${e.message}")
                    }
                }
            }
        }
    }

    override fun onRelayWant(from: String, segmentId: String) {
        // Relay WS desativado — segmentos só via DataChannel.
    }

    override fun onRelayChunk(from: String, segmentId: String, offset: Int, total: Int, dataB64: String) {}

    override fun onRelayDone(from: String, segmentId: String) {}

    override fun onRelayNack(from: String, segmentId: String) {}

    override fun onClosed() {
        Log.w(tag, "signaling closed (will reconnect)")
        signaledPeerIds.clear()
        refreshPeerCount()
    }

    private fun handleOffer(from: String, sdp: String?) {
        if (sdp == null) return
        val slot = peers.getOrPut(from) { createSlot(from) }
        slot.pc.setRemoteDescription(object : SdpObserverAdapter() {
            override fun onSetSuccess() {
                slot.drainCandidates()
                slot.pc.createAnswer(object : SdpObserverAdapter() {
                    override fun onCreateSuccess(desc: SessionDescription?) {
                        if (desc == null) return
                        slot.pc.setLocalDescription(object : SdpObserverAdapter() {
                            override fun onSetSuccess() {
                                signaling.sendSignal(from, "answer", desc.description, null)
                            }
                        }, desc)
                    }

                    override fun onCreateFailure(p0: String?) {
                        Log.e(tag, "createAnswer failed: $p0")
                    }
                }, MediaConstraints())
            }

            override fun onSetFailure(p0: String?) {
                Log.e(tag, "setRemote(offer) failed: $p0")
            }
        }, SessionDescription(SessionDescription.Type.OFFER, sdp))
    }

    private fun handleAnswer(from: String, sdp: String?) {
        if (sdp == null) return
        val slot = peers[from] ?: return
        slot.pc.setRemoteDescription(object : SdpObserverAdapter() {
            override fun onSetSuccess() {
                slot.drainCandidates()
            }

            override fun onSetFailure(p0: String?) {
                Log.e(tag, "setRemote(answer) failed: $p0")
            }
        }, SessionDescription(SessionDescription.Type.ANSWER, sdp))
    }

    private fun handleCandidate(from: String, candidateJson: String?) {
        if (candidateJson == null) return
        val slot = peers.getOrPut(from) { createSlot(from) }
        try {
            val c = JSONObject(candidateJson)
            val ice = IceCandidate(
                c.optString("sdpMid"),
                c.optInt("sdpMLineIndex"),
                c.optString("candidate"),
            )
            if (slot.remoteSet) {
                slot.pc.addIceCandidate(ice)
            } else {
                slot.pendingCandidates.add(ice)
            }
        } catch (e: Exception) {
            Log.e(tag, "bad candidate", e)
        }
    }

    private fun connectAsOfferer(remoteId: String) {
        executor.execute {
            if (peers.containsKey(remoteId)) return@execute
            if (peers.size >= maxPeers) return@execute
            val slot = createSlot(remoteId)
            peers[remoteId] = slot
            val init = DataChannel.Init().apply {
                ordered = true
            }
            slot.channel = slot.pc.createDataChannel("p2p-segments", init)
            attachChannel(remoteId, slot)
            slot.pc.createOffer(object : SdpObserverAdapter() {
                override fun onCreateSuccess(desc: SessionDescription?) {
                    if (desc == null) return
                    slot.pc.setLocalDescription(object : SdpObserverAdapter() {
                        override fun onSetSuccess() {
                            signaling.sendSignal(remoteId, "offer", desc.description, null)
                        }
                    }, desc)
                }

                override fun onCreateFailure(p0: String?) {
                    Log.e(tag, "createOffer failed: $p0")
                }
            }, MediaConstraints())
        }
    }

    private fun createSlot(remoteId: String): PeerSlot {
        val rtcConfig = PeerConnection.RTCConfiguration(buildIceServers()).apply {
            sdpSemantics = PeerConnection.SdpSemantics.UNIFIED_PLAN
            continualGatheringPolicy = PeerConnection.ContinualGatheringPolicy.GATHER_CONTINUALLY
            tcpCandidatePolicy = PeerConnection.TcpCandidatePolicy.ENABLED
            bundlePolicy = PeerConnection.BundlePolicy.MAXBUNDLE
        }
        val slot = PeerSlot(remoteId)
        slot.pc = factory.createPeerConnection(rtcConfig, object : PeerConnection.Observer {
            override fun onSignalingChange(state: PeerConnection.SignalingState?) {}
            override fun onIceConnectionChange(state: PeerConnection.IceConnectionState?) {
                Log.d(tag, "ICE $remoteId -> $state")
                when (state) {
                    PeerConnection.IceConnectionState.CONNECTED,
                    PeerConnection.IceConnectionState.COMPLETED,
                    -> {
                        iceRetries.remove(remoteId)
                        refreshPeerCount()
                    }
                    PeerConnection.IceConnectionState.FAILED -> {
                        peers.remove(remoteId)?.close()
                        refreshPeerCount()
                        val n = iceRetries.merge(remoteId, 1) { a, _ -> a + 1 } ?: 1
                        Log.w(tag, "WebRTC ICE FAILED for ${remoteId.take(8)} (try=$n) — confira STUN/TURN")
                        if (n <= 2 && signaledPeerIds.contains(remoteId) && localPeerId < remoteId) {
                            executor.execute {
                                try {
                                    Thread.sleep(1_500L * n)
                                } catch (_: InterruptedException) {
                                    return@execute
                                }
                                if (signaledPeerIds.contains(remoteId) && !peers.containsKey(remoteId)) {
                                    Log.i(tag, "ICE retry connectAsOfferer ${remoteId.take(8)}")
                                    connectAsOfferer(remoteId)
                                }
                            }
                        }
                    }
                    PeerConnection.IceConnectionState.CLOSED -> {
                        peers.remove(remoteId)
                        refreshPeerCount()
                    }
                    else -> refreshPeerCount()
                }
            }
            override fun onIceConnectionReceivingChange(p0: Boolean) {}
            override fun onIceGatheringChange(p0: PeerConnection.IceGatheringState?) {}
            override fun onIceCandidate(candidate: IceCandidate?) {
                if (candidate == null) return
                val json = JSONObject()
                    .put("sdpMid", candidate.sdpMid)
                    .put("sdpMLineIndex", candidate.sdpMLineIndex)
                    .put("candidate", candidate.sdp)
                signaling.sendSignal(remoteId, "candidate", null, json.toString())
            }
            override fun onIceCandidatesRemoved(p0: Array<out IceCandidate>?) {}
            override fun onAddStream(p0: org.webrtc.MediaStream?) {}
            override fun onRemoveStream(p0: org.webrtc.MediaStream?) {}
            override fun onDataChannel(dc: DataChannel?) {
                if (dc == null) return
                slot.channel = dc
                attachChannel(remoteId, slot)
            }
            override fun onRenegotiationNeeded() {}
            override fun onAddTrack(p0: org.webrtc.RtpReceiver?, p1: Array<out org.webrtc.MediaStream>?) {}
        })!!
        return slot
    }

    private fun attachChannel(remoteId: String, slot: PeerSlot) {
        val ch = slot.channel ?: return
        ch.registerObserver(object : DataChannel.Observer {
            override fun onBufferedAmountChange(p0: Long) {}
            override fun onStateChange() {
                Log.d(tag, "DC $remoteId -> ${ch.state()}")
                if (ch.state() == DataChannel.State.OPEN) {
                    announceInventory()
                    refreshPeerCount()
                }
            }
            override fun onMessage(buffer: DataChannel.Buffer?) {
                if (buffer == null) return
                val bytes = ByteArray(buffer.data.remaining())
                buffer.data.get(bytes)
                trafficStats.addP2pDown(bytes.size.toLong())
                handleChannelMessage(remoteId, slot, bytes)
            }
        })
    }

    private fun handleChannelMessage(from: String, slot: PeerSlot, bytes: ByteArray) {
        val text = String(bytes, StandardCharsets.UTF_8)
        val json = try {
            JSONObject(text)
        } catch (_: Exception) {
            return
        }
        when (json.optString("t")) {
            "have" -> {
                val arr = json.optJSONArray("ids") ?: JSONArray()
                val set = remoteInventory.getOrPut(from) { ConcurrentHashMap.newKeySet() }
                for (i in 0 until arr.length()) set.add(arr.getString(i))
                synchronized(inventoryLock) { inventoryLock.notifyAll() }
            }
            "want" -> {
                val id = json.getString("id")
                val data = store.get(id)
                if (data == null) {
                    sendJson(slot, JSONObject().put("t", "nack").put("id", id).put("reason", "missing"))
                    return
                }
                val chunkSize = 12 * 1024
                var offset = 0
                while (offset < data.size) {
                    val end = (offset + chunkSize).coerceAtMost(data.size)
                    val part = data.copyOfRange(offset, end)
                    val msg = JSONObject()
                        .put("t", "chunk")
                        .put("id", id)
                        .put("offset", offset)
                        .put("total", data.size)
                        .put("data", Base64.encodeToString(part, Base64.NO_WRAP))
                    sendJson(slot, msg)
                    offset = end
                }
                sendJson(slot, JSONObject().put("t", "done").put("id", id))
            }
            "chunk" -> {
                val id = json.getString("id")
                val pendingDownload = pending[id] ?: return
                val offset = json.getInt("offset")
                val total = json.getInt("total")
                val part = Base64.decode(json.getString("data"), Base64.NO_WRAP)
                pendingDownload.accept(offset, total, part)
            }
            "done" -> {
                val id = json.getString("id")
                pending[id]?.markDone()
            }
            "nack" -> {
                val id = json.getString("id")
                pending[id]?.fail()
            }
        }
    }

    private fun sendJson(slot: PeerSlot, obj: JSONObject) {
        val bytes = obj.toString().toByteArray(StandardCharsets.UTF_8)
        val ch = slot.channel ?: return
        if (ch.state() != DataChannel.State.OPEN) return
        trafficStats.addP2pUp(bytes.size.toLong())
        ch.send(DataChannel.Buffer(ByteBuffer.wrap(bytes), false))
    }

    private fun refreshPeerCount() {
        val open = peers.values.count { it.channel?.state() == DataChannel.State.OPEN }
        trafficStats.signaledPeers = signaledPeerIds.size
        trafficStats.connectedPeers = open
        // Overlay: mostra peers descobertos na sinalização (mesmo sem WebRTC)
        trafficStats.peerCount = signaledPeerIds.size.coerceAtLeast(open)
    }

    private fun buildIceServers(): List<PeerConnection.IceServer> {
        if (iceServers.isEmpty()) {
            return listOf(
                PeerConnection.IceServer.builder("stun:stun.l.google.com:19302").createIceServer()
            )
        }
        return iceServers.map { cfg ->
            val urls = cfg.urls
            val b = if (urls.size == 1) {
                PeerConnection.IceServer.builder(urls[0])
            } else {
                PeerConnection.IceServer.builder(urls)
            }
            if (!cfg.username.isNullOrBlank()) {
                b.setUsername(cfg.username).setPassword(cfg.credential ?: "")
            }
            b.createIceServer()
        }
    }

    private class PeerSlot(val remoteId: String) {
        lateinit var pc: PeerConnection
        var channel: DataChannel? = null
        @Volatile var remoteSet = false
        val pendingCandidates = mutableListOf<IceCandidate>()

        fun drainCandidates() {
            remoteSet = true
            pendingCandidates.forEach { pc.addIceCandidate(it) }
            pendingCandidates.clear()
        }

        fun close() {
            try {
                channel?.close()
                pc.close()
            } catch (_: Exception) {
            }
        }
    }

    private class PendingDownload {
        private val lock = Object()
        @Volatile var data: ByteArray? = null
        private var buffer: ByteArray? = null
        @Volatile private var done = false
        @Volatile private var failed = false

        fun accept(offset: Int, total: Int, part: ByteArray) {
            synchronized(lock) {
                if (buffer == null || buffer!!.size != total) {
                    buffer = ByteArray(total)
                }
                System.arraycopy(part, 0, buffer!!, offset, part.size)
            }
        }

        fun markDone() {
            synchronized(lock) {
                data = buffer
                done = true
                lock.notifyAll()
            }
        }

        fun fail() {
            synchronized(lock) {
                failed = true
                lock.notifyAll()
            }
        }

        fun waitDone(timeoutMs: Long): Boolean {
            val deadline = System.currentTimeMillis() + timeoutMs
            synchronized(lock) {
                while (!done && !failed) {
                    val left = deadline - System.currentTimeMillis()
                    if (left <= 0) return false
                    lock.wait(left)
                }
                return done && !failed && data != null
            }
        }
    }

    open class SdpObserverAdapter : org.webrtc.SdpObserver {
        override fun onCreateSuccess(p0: SessionDescription?) {}
        override fun onSetSuccess() {}
        override fun onCreateFailure(p0: String?) {}
        override fun onSetFailure(p0: String?) {}
    }
}
