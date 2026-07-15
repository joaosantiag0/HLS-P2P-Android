package br.com.p2pplayer

import br.com.p2pplayer.net.formatBytes
import br.com.p2pplayer.net.formatRate
import br.com.p2pplayer.p2p.SegmentStore
import br.com.p2pplayer.player.HybridDataSource
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class SegmentStoreTest {
	@Test
	fun putKeyedStoresById() {
		val dir = File.createTempFile("seg", "dir").apply {
			delete()
			mkdir()
		}
		val store = SegmentStore(dir, maxBytes = 1024)
		val data = "abc".toByteArray()
		val id = "a".repeat(64)
		assertTrue(store.putKeyed(id, data))
		assertTrue(store.has(id))
		assertNotNull(store.get(id))
	}

    @Test
    fun shaStable() {
        val a = SegmentStore.sha256Hex("x".toByteArray())
        val b = SegmentStore.sha256Hex("x".toByteArray())
        assertEquals(a, b)
        assertEquals(64, a.length)
    }
}

class HybridDataSourceTest {
    @Test
    fun extractSegmentId() {
        val id = "a".repeat(64)
        assertEquals(
            id,
            HybridDataSource.extractSegmentId("http://host/hls/s/seg/$id.ts"),
        )
        assertNull(HybridDataSource.extractSegmentId("http://host/hls/s/index.m3u8"))
    }
}

class FormatTest {
    @Test
    fun formats() {
        assertEquals("512B", formatBytes(512))
        assertTrue(formatRate(2048.0).contains("KB"))
    }
}
