package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/p2pplayer/server/internal/cache"
	"github.com/p2pplayer/server/internal/config"
	"github.com/p2pplayer/server/internal/session"
	"github.com/p2pplayer/server/internal/signaling"
)

type Server struct {
	cfg      config.Config
	sessions *session.Manager
	hub      *signaling.Hub
	cache    *cache.Store
	mux      *http.ServeMux
}

func New(cfg config.Config, sessions *session.Manager, hub *signaling.Hub, store *cache.Store) *Server {
	s := &Server{cfg: cfg, sessions: sessions, hub: hub, cache: store, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.withAccess(s.withLogging(s.mux))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("POST /api/v1/sessions", s.handleCreateSession)
	s.mux.HandleFunc("GET /hls/{sessionId}/index.m3u8", s.handleManifest)
	s.mux.HandleFunc("GET /hls/{sessionId}/variant", s.handleVariant)
	s.mux.HandleFunc("GET /hls/{sessionId}/seg/{segmentId}", s.handleSegment)
	s.mux.HandleFunc("GET /hls/{sessionId}/u/{urlKey}", s.handleURLSegment)
	s.mux.HandleFunc("GET /hls/{sessionId}/fetch", s.handleFetch)
	s.mux.HandleFunc("GET /ws", s.hub.ServeWS)
}

type createBody struct {
	URL       string `json:"url"`
	UserAgent string `json:"userAgent"`
}

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type createResp struct {
	SessionID   string      `json:"sessionId"`
	StreamKey   string      `json:"streamKey"`
	ManifestURL string      `json:"manifestUrl"`
	WSURL       string      `json:"wsUrl"`
	ICEServers  []iceServer `json:"iceServers"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	count, total := s.cache.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"cacheCount":  count,
		"cacheBytes":  total,
		"time":        time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body createBody
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.URL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	sess, err := s.sessions.Create(r.Context(), session.CreateRequest{
		URL:       body.URL,
		UserAgent: body.UserAgent,
	})
	if err != nil {
		log.Printf("create session error: %v", err)
		code := http.StatusBadGateway
		msg := err.Error()
		hint := "verifique URL HLS, user/senha e User-Agent"
		var oe *session.OriginError
		if errors.As(err, &oe) {
			if oe.Status == http.StatusForbidden {
				hint = "origem retornou 403 — UA bloqueado, IP do servidor sem acesso, ou credenciais inválidas"
			}
		}
		writeJSON(w, code, map[string]any{
			"error": msg,
			"hint":  hint,
		})
		return
	}
	writeJSON(w, http.StatusOK, createResp{
		SessionID:   sess.ID,
		StreamKey:   sess.StreamKey,
		ManifestURL: "/hls/" + sess.ID + "/index.m3u8",
		WSURL:       "/ws",
		ICEServers:  s.iceServers(),
	})
}

func (s *Server) iceServers() []iceServer {
	out := make([]iceServer, 0, 2)
	if len(s.cfg.STUNURLs) > 0 {
		out = append(out, iceServer{URLs: s.cfg.STUNURLs})
	}
	if s.cfg.TURNURL != "" {
		out = append(out, iceServer{
			URLs:       []string{s.cfg.TURNURL},
			Username:   s.cfg.TURNUser,
			Credential: s.cfg.TURNPass,
		})
	}
	return out
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionId")
	body, err := s.sessions.Manifest(r.Context(), id)
	if err != nil {
		http.Error(w, "manifest error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(body))
}

func (s *Server) handleVariant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionId")
	u := r.URL.Query().Get("u")
	if u == "" {
		http.Error(w, "missing u", http.StatusBadRequest)
		return
	}
	body, err := s.sessions.Variant(r.Context(), id, u)
	if err != nil {
		http.Error(w, "variant error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(body))
}

func (s *Server) handleSegment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionId")
	seg := r.PathValue("segmentId")
	seg = strings.TrimSuffix(seg, ".ts")
	f, size, err := s.sessions.OpenSegment(id, seg)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Content-Length", itoa(size))
	w.Header().Set("Cache-Control", "public, max-age=60")
	_, _ = io.Copy(w, f)
}

func (s *Server) handleURLSegment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionId")
	key := strings.TrimSuffix(r.PathValue("urlKey"), ".ts")
	_, data, err := s.sessions.OpenByURLKey(r.Context(), id, key)
	if err != nil {
		http.Error(w, "segment error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Content-Length", itoa(int64(len(data))))
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionId")
	u := r.URL.Query().Get("u")
	if u == "" {
		http.Error(w, "missing u", http.StatusBadRequest)
		return
	}
	_, data, err := s.sessions.FetchAndCache(r.Context(), id, u)
	if err != nil {
		http.Error(w, "fetch error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Content-Length", itoa(int64(len(data))))
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func (s *Server) withAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AccessSecret == "" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get("X-Access-Secret")
		if key == "" {
			key = r.URL.Query().Get("access")
		}
		if key != s.cfg.AccessSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
