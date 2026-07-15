package session_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/p2pplayer/server/internal/cache"
	"github.com/p2pplayer/server/internal/config"
	"github.com/p2pplayer/server/internal/hls"
	"github.com/p2pplayer/server/internal/session"
)

func TestCreateSessionAgainstLocalOrigin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\na.ts\n"))
	})
	mux.HandleFunc("/a.ts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("mpegts-segment-bytes"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	store, err := cache.New(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		CacheDir:        dir,
		CacheMaxBytes:   1 << 20,
		SessionTTL:      time.Hour,
		MaxRedirects:    3,
		AllowPrivateNet: true,
		MaxSegmentBytes: 1 << 20,
		OriginTimeout:   5 * time.Second,
	}
	mgr := session.NewManager(cfg, store)
	sess, err := mgr.Create(context.Background(), session.CreateRequest{
		URL:       srv.URL + "/index.m3u8",
		UserAgent: "TestUA/1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := mgr.Manifest(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := hls.URLKey(srv.URL + "/a.ts")
	if !strings.Contains(body, "/u/"+key) {
		t.Fatalf("expected /u/ key in playlist, got %q", body)
	}
	_, data, err := mgr.OpenByURLKey(context.Background(), sess.ID, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "mpegts-segment-bytes" {
		t.Fatalf("got %q", data)
	}
}
