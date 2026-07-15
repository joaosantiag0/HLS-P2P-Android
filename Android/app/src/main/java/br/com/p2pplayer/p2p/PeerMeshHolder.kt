package br.com.p2pplayer.p2p

import java.util.concurrent.atomic.AtomicReference

/** Holder mutável para o PeerMesh ativo (recriado no ciclo de vida). */
object PeerMeshHolder {
    val current = AtomicReference<PeerMesh?>(null)
}
