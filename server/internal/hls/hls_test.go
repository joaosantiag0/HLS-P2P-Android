package hls_test

import (
	"strings"
	"testing"

	"github.com/p2pplayer/server/internal/hls"
)

func TestStreamKeyXtreamHidesCredentials(t *testing.T) {
	u := "http://cdn.example.com:8080/live/user123/pass456/7890.m3u8"
	key := hls.StreamKey(u)
	if strings.Contains(key, "user123") || strings.Contains(key, "pass456") {
		t.Fatalf("credentials leaked in streamKey: %s", key)
	}
	if !strings.Contains(key, "7890") {
		t.Fatalf("expected stream id in key: %s", key)
	}
	if !strings.HasPrefix(key, "cdn.example.com|live|") {
		t.Fatalf("unexpected key: %s", key)
	}
}

func TestStreamKeyOpaque(t *testing.T) {
	u := "https://cdn.example.com/playlist/abc.m3u8?token=secret"
	key := hls.StreamKey(u)
	if !strings.HasPrefix(key, "opaque:") {
		t.Fatalf("expected opaque key, got %s", key)
	}
	if strings.Contains(key, "secret") {
		t.Fatal("token leaked")
	}
}

func TestRewritePlaylistSegments(t *testing.T) {
	body := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXTINF:6.0,
seg0.ts
#EXTINF:6.0,
https://cdn.example.com/live/seg1.ts
`
	base := "https://cdn.example.com/live/index.m3u8"
	ids := map[string]string{
		"https://cdn.example.com/live/seg0.ts": "aaa",
		"https://cdn.example.com/live/seg1.ts": "bbb",
	}
	res, err := hls.RewritePlaylist(body, base, "sess1", ids)
	if err != nil {
		t.Fatal(err)
	}
	k0 := hls.URLKey("https://cdn.example.com/live/seg0.ts")
	k1 := hls.URLKey("https://cdn.example.com/live/seg1.ts")
	if !strings.Contains(res.Body, "/hls/sess1/u/"+k0+".ts") {
		t.Fatalf("missing rewritten seg0: %s", res.Body)
	}
	if !strings.Contains(res.Body, "/hls/sess1/u/"+k1+".ts") {
		t.Fatalf("missing rewritten seg1: %s", res.Body)
	}
	if res.IsMaster {
		t.Fatal("should be media playlist")
	}
}

func TestCollectAndPickVariant(t *testing.T) {
	body := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000
low.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000
high.m3u8
`
	segs, playlists, err := hls.CollectURIs(body, "https://cdn.example.com/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 0 {
		t.Fatalf("unexpected segs: %v", segs)
	}
	if len(playlists) != 2 {
		t.Fatalf("playlists=%v", playlists)
	}
	picked := hls.PickBestVariant(playlists)
	if picked != "https://cdn.example.com/low.m3u8" {
		t.Fatalf("picked=%s", picked)
	}
}
