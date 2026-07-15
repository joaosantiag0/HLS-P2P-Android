package br.com.p2pplayer.p2p

import android.content.Context
import org.webrtc.DefaultVideoDecoderFactory
import org.webrtc.DefaultVideoEncoderFactory
import org.webrtc.PeerConnectionFactory
import java.util.concurrent.atomic.AtomicBoolean

object WebRtcFactory {
    private val initialized = AtomicBoolean(false)
    @Volatile private var factory: PeerConnectionFactory? = null

    fun get(context: Context): PeerConnectionFactory {
        if (initialized.compareAndSet(false, true)) {
            PeerConnectionFactory.initialize(
                PeerConnectionFactory.InitializationOptions.builder(context.applicationContext)
                    .createInitializationOptions()
            )
            factory = PeerConnectionFactory.builder()
                .setOptions(PeerConnectionFactory.Options())
                .setVideoEncoderFactory(DefaultVideoEncoderFactory(null, false, false))
                .setVideoDecoderFactory(DefaultVideoDecoderFactory(null))
                .createPeerConnectionFactory()
        }
        return factory!!
    }
}
