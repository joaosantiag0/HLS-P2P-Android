package br.com.p2pplayer.p2p

import java.io.File
import java.security.MessageDigest
import java.util.concurrent.ConcurrentHashMap

class SegmentStore(private val dir: File, private val maxBytes: Long = 64L * 1024 * 1024) {
    private val index = ConcurrentHashMap<String, Long>()
    private var total = 0L

    init {
        if (!dir.exists()) dir.mkdirs()
        dir.listFiles()?.forEach { f ->
            if (f.isFile && f.name.length == 64) {
                index[f.name] = f.length()
                total += f.length()
            }
        }
        evict()
    }

    fun has(id: String): Boolean = index.containsKey(id) || File(dir, id).exists()

    fun get(id: String): ByteArray? {
        val f = File(dir, id)
        if (!f.exists()) return null
        return f.readBytes()
    }

    /** Guarda por id arbitrário (ex.: hash da URL de origem para P2P live). */
    fun putKeyed(id: String, data: ByteArray): Boolean {
        if (id.length != 64) return false
        val f = File(dir, id)
        if (!f.exists()) {
            f.writeBytes(data)
            index[id] = data.size.toLong()
            total += data.size.toLong()
            evict()
        } else {
            // atualiza timestamp de uso
            f.setLastModified(System.currentTimeMillis())
        }
        return true
    }

    fun put(id: String, data: ByteArray): Boolean {
        val hash = sha256Hex(data)
        if (hash != id) return putKeyed(id, data) // url-key: aceita mesmo assim
        return putKeyed(id, data)
    }

    fun putRaw(data: ByteArray): String {
        val id = sha256Hex(data)
        putKeyed(id, data)
        return id
    }

    fun knownIds(): Set<String> = index.keys.toSet()

    /** IDs mais recentes primeiro (para inventário P2P live). */
    fun recentIds(limit: Int): List<String> {
        return dir.listFiles()
            ?.filter { it.isFile && index.containsKey(it.name) }
            ?.sortedByDescending { it.lastModified() }
            ?.take(limit)
            ?.map { it.name }
            ?: emptyList()
    }

    private fun evict() {
        while (total > maxBytes && index.isNotEmpty()) {
            val oldest = dir.listFiles()
                ?.filter { it.isFile && index.containsKey(it.name) }
                ?.minByOrNull { it.lastModified() }
                ?: break
            val size = index.remove(oldest.name) ?: oldest.length()
            total -= size
            oldest.delete()
        }
    }

    companion object {
        fun sha256Hex(data: ByteArray): String {
            val digest = MessageDigest.getInstance("SHA-256").digest(data)
            return digest.joinToString("") { "%02x".format(it) }
        }
    }
}
