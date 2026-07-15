package br.com.p2pplayer

import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.View
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.lifecycle.lifecycleScope
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.exoplayer.DefaultLoadControl
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.hls.HlsMediaSource
import androidx.media3.ui.PlayerView
import br.com.p2pplayer.net.ApiClient
import br.com.p2pplayer.net.TrafficStats
import br.com.p2pplayer.net.formatBytes
import br.com.p2pplayer.net.formatRate
import br.com.p2pplayer.p2p.PeerMesh
import br.com.p2pplayer.p2p.PeerMeshHolder
import br.com.p2pplayer.p2p.SegmentStore
import br.com.p2pplayer.p2p.SignalingClient
import br.com.p2pplayer.player.HybridDataSource
import com.google.android.material.button.MaterialButton
import com.google.android.material.textfield.TextInputEditText
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.util.UUID

class MainActivity : AppCompatActivity() {

    private lateinit var formPanel: View
    private lateinit var inputServer: TextInputEditText
    private lateinit var inputUrl: TextInputEditText
    private lateinit var inputUserAgent: TextInputEditText
    private lateinit var btnPlay: MaterialButton
    private lateinit var errorText: TextView
    private lateinit var playerView: PlayerView
    private lateinit var statsOverlay: TextView
    private lateinit var loading: ProgressBar

    private var player: ExoPlayer? = null
    private var peerMesh: PeerMesh? = null
    private var signaling: SignalingClient? = null
    private var lastServer: String? = null
    private var lastSession: br.com.p2pplayer.net.SessionResponse? = null
    private var lastPeerId: String? = null
    private val trafficStats = TrafficStats()
    private val handler = Handler(Looper.getMainLooper())
    private val statsTicker = object : Runnable {
        override fun run() {
            trafficStats.tickRates()
            val s = trafficStats.snapshot()
            statsOverlay.text = getString(
                R.string.stats_format,
                formatRate(s.httpDownRate), formatBytes(s.httpDownTotal),
                formatRate(s.httpUpRate), formatBytes(s.httpUpTotal),
                formatRate(s.p2pDownRate), formatBytes(s.p2pDownTotal),
                formatRate(s.p2pUpRate), formatBytes(s.p2pUpTotal),
                s.signaledPeers,
                s.connectedPeers,
            )
            handler.postDelayed(this, 1000)
        }
    }

    private lateinit var segmentStore: SegmentStore

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        ViewCompat.setOnApplyWindowInsetsListener(findViewById(R.id.main)) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            insets
        }

        formPanel = findViewById(R.id.formPanel)
        inputServer = findViewById(R.id.inputServer)
        inputUrl = findViewById(R.id.inputUrl)
        inputUserAgent = findViewById(R.id.inputUserAgent)
        btnPlay = findViewById(R.id.btnPlay)
        errorText = findViewById(R.id.errorText)
        playerView = findViewById(R.id.playerView)
        statsOverlay = findViewById(R.id.statsOverlay)
        loading = findViewById(R.id.loading)

        inputServer.setText(BuildConfig.DEFAULT_SERVER_URL)
        inputUserAgent.setText(getString(R.string.default_user_agent))

        segmentStore = SegmentStore(File(cacheDir, "segments"))

        btnPlay.setOnClickListener { startPlayback() }
        btnPlay.requestFocus()

        // Teste via adb: am start -n br.com.p2pplayer/.MainActivity --es url "…m3u8" [--es server "http://…"]
        intent.getStringExtra("server")?.takeIf { it.isNotBlank() }?.let { inputServer.setText(it) }
        intent.getStringExtra("url")?.takeIf { it.isNotBlank() }?.let {
            inputUrl.setText(it)
            if (intent.getBooleanExtra("autoplay", true)) {
                btnPlay.post { startPlayback() }
            }
        }
    }

    private fun startPlayback() {
        val server = inputServer.text?.toString()?.trim().orEmpty()
        val url = inputUrl.text?.toString()?.trim().orEmpty()
        val ua = inputUserAgent.text?.toString()?.trim().orEmpty()
            .ifEmpty { getString(R.string.default_user_agent) }

        if (server.isEmpty() || url.isEmpty()) {
            showError("Preencha servidor e URL do stream")
            return
        }
        if (!url.contains(".m3u8", ignoreCase = true)) {
            showError("MVP aceita apenas HLS (.m3u8)")
            return
        }

        errorText.visibility = View.GONE
        loading.visibility = View.VISIBLE
        btnPlay.isEnabled = false

        lifecycleScope.launch {
            try {
                val session = withContext(Dispatchers.IO) {
                    ApiClient(server).createSession(url, ua)
                }
                lastServer = server
                lastSession = session
                lastPeerId = UUID.randomUUID().toString()
                startMesh(server, session, lastPeerId!!)

                val manifest = ApiClient(server).absoluteUrl(session.manifestUrl)
                setupPlayer(manifest)
                formPanel.visibility = View.GONE
                playerView.visibility = View.VISIBLE
                statsOverlay.visibility = View.VISIBLE
                handler.post(statsTicker)
            } catch (e: Exception) {
                showError(getString(R.string.error_play, e.message ?: e.javaClass.simpleName))
            } finally {
                loading.visibility = View.GONE
                btnPlay.isEnabled = true
            }
        }
    }

    private fun setupPlayer(manifestUrl: String) {
        releasePlayer()
        val loadControl = DefaultLoadControl.Builder()
            .setBufferDurationsMs(
                /* minBufferMs */ 2_500,
                /* maxBufferMs */ 10_000,
                /* bufferForPlaybackMs */ 1_000,
                /* bufferForPlaybackAfterRebufferMs */ 1_500,
            )
            .setTargetBufferBytes(2 * 1024 * 1024)
            .build()

        val exo = ExoPlayer.Builder(this)
            .setLoadControl(loadControl)
            .build()
        player = exo
        playerView.player = exo

        val mediaItem = MediaItem.Builder()
            .setUri(manifestUrl)
            .setLiveConfiguration(
                MediaItem.LiveConfiguration.Builder()
                    .setTargetOffsetMs(8_000)
                    .setMinOffsetMs(3_000)
                    .setMaxOffsetMs(20_000)
                    .setMaxPlaybackSpeed(1.08f)
                    .build()
            )
            .build()

        val dataSourceFactory = HybridDataSource.Factory(segmentStore, trafficStats)
        val mediaSource = HlsMediaSource.Factory(dataSourceFactory)
            .setAllowChunklessPreparation(true)
            .createMediaSource(mediaItem)

        exo.addListener(object : Player.Listener {
            override fun onPlayerError(error: PlaybackException) {
                if (error.errorCode == PlaybackException.ERROR_CODE_BEHIND_LIVE_WINDOW) {
                    exo.seekToDefaultPosition()
                    exo.prepare()
                    return
                }
                Toast.makeText(
                    this@MainActivity,
                    "Erro de playback: ${error.errorCodeName}",
                    Toast.LENGTH_LONG,
                ).show()
            }
        })
        exo.setMediaSource(mediaSource)
        exo.prepare()
        exo.playWhenReady = true
    }

    private fun showError(msg: String) {
        errorText.text = msg
        errorText.visibility = View.VISIBLE
    }

    private fun releasePlayer() {
        player?.release()
        player = null
        playerView.player = null
    }

    private fun startMesh(
        server: String,
        session: br.com.p2pplayer.net.SessionResponse,
        peerId: String,
    ): PeerMesh {
        stopP2p()
        val ws = ApiClient(server).wsAbsoluteUrl(session.wsUrl)
        val sig = SignalingClient(
            wsUrl = ws,
            peerId = peerId,
            streamKey = session.streamKey,
            sessionId = session.sessionId,
            trafficStats = trafficStats,
        )
        signaling = sig
        val mesh = PeerMesh(
            context = this,
            localPeerId = peerId,
            iceServers = session.iceServers,
            signaling = sig,
            store = segmentStore,
            trafficStats = trafficStats,
        )
        peerMesh = mesh
        PeerMeshHolder.current.set(mesh)
        mesh.start()
        return mesh
    }

    private fun stopP2p() {
        PeerMeshHolder.current.set(null)
        peerMesh?.stop()
        peerMesh = null
        signaling?.close()
        signaling = null
    }

    override fun onStop() {
        super.onStop()
        // Mantém mesh/sinalização vivos (TV box: onStop acontece com overlays).
        player?.playWhenReady = false
    }

    override fun onStart() {
        super.onStart()
        if (player != null) {
            player?.playWhenReady = true
        }
    }

    override fun onDestroy() {
        handler.removeCallbacks(statsTicker)
        stopP2p()
        releasePlayer()
        super.onDestroy()
    }
}
