package session

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/p2pplayer/server/internal/cache"
	"github.com/p2pplayer/server/internal/config"
	"github.com/p2pplayer/server/internal/hls"
	"github.com/p2pplayer/server/internal/ssrf"
)

type Session struct {
	ID         string
	OriginURL  string
	UserAgent  string
	StreamKey  string
	MediaURL   string // resolved media playlist URL
	CreatedAt  time.Time
	LastAccess time.Time
	mu         sync.Mutex
	segMap     map[string]string // originAbsURL -> contentHash (opcional)
	urlMap     map[string]string // urlKey -> originAbsURL
	cachedBody string
	isMaster   bool
}

type Manager struct {
	cfg    config.Config
	cache  *cache.Store
	client *http.Client
	mu     sync.RWMutex
	byID   map[string]*Session
}

func NewManager(cfg config.Config, store *cache.Store) *Manager {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          32,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	client := &http.Client{
		Timeout:   cfg.OriginTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= cfg.MaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			if _, err := ssrf.ValidateURL(req.URL.String(), cfg.AllowPrivateNet); err != nil {
				return err
			}
			return nil
		},
	}
	m := &Manager{
		cfg:    cfg,
		cache:  store,
		client: client,
		byID:   make(map[string]*Session),
	}
	go m.reaper()
	return m
}

func (m *Manager) reaper() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		now := time.Now()
		for id, s := range m.byID {
			s.mu.Lock()
			idle := now.Sub(s.LastAccess)
			s.mu.Unlock()
			if idle > m.cfg.SessionTTL {
				delete(m.byID, id)
			}
		}
		m.mu.Unlock()
	}
}

type CreateRequest struct {
	URL       string
	UserAgent string
}

func (m *Manager) Create(ctx context.Context, req CreateRequest) (*Session, error) {
	if _, err := ssrf.ValidateURL(req.URL, m.cfg.AllowPrivateNet); err != nil {
		return nil, fmt.Errorf("url rejected: %w", err)
	}
	ua := ssrf.SanitizeUserAgent(req.UserAgent)
	body, finalURL, err := m.fetch(ctx, req.URL, ua)
	if err != nil {
		return nil, err
	}
	segs, playlists, err := hls.CollectURIs(string(body), finalURL)
	if err != nil {
		return nil, err
	}
	mediaURL := finalURL
	if len(playlists) > 0 {
		mediaURL = hls.PickBestVariant(playlists)
		body, mediaURL, err = m.fetch(ctx, mediaURL, ua)
		if err != nil {
			return nil, err
		}
		segs, _, err = hls.CollectURIs(string(body), mediaURL)
		if err != nil {
			return nil, err
		}
	}
	_ = segs
	s := &Session{
		ID:         uuid.NewString(),
		OriginURL:  req.URL,
		UserAgent:  ua,
		StreamKey:  hls.StreamKey(req.URL),
		MediaURL:   mediaURL,
		CreatedAt:  time.Now(),
		LastAccess: time.Now(),
		segMap:     make(map[string]string),
		urlMap:     make(map[string]string),
	}
	m.registerURLs(s, string(body), mediaURL)
	// Live: não pré-baixa todos os segmentos (atrasa o manifest e causa behind-live).
	rewritten, err := hls.RewritePlaylist(string(body), mediaURL, s.ID, s.segMap)
	if err != nil {
		return nil, err
	}
	s.cachedBody = rewritten.Body
	s.isMaster = rewritten.IsMaster

	m.mu.Lock()
	m.byID[s.ID] = s
	m.mu.Unlock()
	return s, nil
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	s, ok := m.byID[id]
	m.mu.RUnlock()
	if ok {
		s.mu.Lock()
		s.LastAccess = time.Now()
		s.mu.Unlock()
	}
	return s, ok
}

func (m *Manager) Manifest(ctx context.Context, id string) (string, error) {
	s, ok := m.Get(id)
	if !ok {
		return "", fmt.Errorf("session not found")
	}
	body, finalURL, err := m.fetch(ctx, s.MediaURL, s.UserAgent)
	if err != nil {
		// Em live, playlist antiga empurra o player para trás da janela — não servir stale.
		return "", err
	}
	m.registerURLs(s, string(body), finalURL)
	s.mu.Lock()
	segMap := copyMap(s.segMap)
	s.mu.Unlock()
	rewritten, err := hls.RewritePlaylist(string(body), finalURL, s.ID, segMap)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.cachedBody = rewritten.Body
	s.MediaURL = finalURL
	s.mu.Unlock()
	return rewritten.Body, nil
}

func (m *Manager) Variant(ctx context.Context, id, variantURL string) (string, error) {
	s, ok := m.Get(id)
	if !ok {
		return "", fmt.Errorf("session not found")
	}
	if _, err := ssrf.ValidateURL(variantURL, m.cfg.AllowPrivateNet); err != nil {
		return "", err
	}
	body, finalURL, err := m.fetch(ctx, variantURL, s.UserAgent)
	if err != nil {
		return "", err
	}
	m.registerURLs(s, string(body), finalURL)
	s.mu.Lock()
	segMap := copyMap(s.segMap)
	s.MediaURL = finalURL
	s.mu.Unlock()
	rewritten, err := hls.RewritePlaylist(string(body), finalURL, s.ID, segMap)
	if err != nil {
		return "", err
	}
	return rewritten.Body, nil
}

func (m *Manager) registerURLs(s *Session, body, baseURL string) {
	segs, _, err := hls.CollectURIs(body, baseURL)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.urlMap == nil {
		s.urlMap = make(map[string]string)
	}
	for _, abs := range segs {
		s.urlMap[hls.URLKey(abs)] = abs
	}
}

func (m *Manager) FetchAndCache(ctx context.Context, id, originURL string) (string, []byte, error) {
	s, ok := m.Get(id)
	if !ok {
		return "", nil, fmt.Errorf("session not found")
	}
	if _, err := ssrf.ValidateURL(originURL, m.cfg.AllowPrivateNet); err != nil {
		return "", nil, err
	}
	key := hls.URLKey(originURL)
	s.mu.Lock()
	if s.urlMap == nil {
		s.urlMap = make(map[string]string)
	}
	s.urlMap[key] = originURL
	s.mu.Unlock()
	return m.OpenByURLKey(ctx, id, key)
}

// OpenByURLKey baixa sob demanda o segmento identificado pelo hash da URL de origem.
func (m *Manager) OpenByURLKey(ctx context.Context, sessionID, urlKey string) (string, []byte, error) {
	s, ok := m.Get(sessionID)
	if !ok {
		return "", nil, fmt.Errorf("session not found")
	}
	s.mu.Lock()
	origin := s.urlMap[urlKey]
	contentID := s.segMap[origin]
	s.mu.Unlock()
	if origin == "" {
		return "", nil, fmt.Errorf("unknown url key")
	}
	if contentID != "" {
		if data, err := m.cache.Get(contentID); err == nil {
			return urlKey, data, nil
		}
	}
	if err := m.ensureSegments(ctx, s, []string{origin}); err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	contentID = s.segMap[origin]
	s.mu.Unlock()
	data, err := m.cache.Get(contentID)
	if err != nil {
		return "", nil, err
	}
	return urlKey, data, nil
}

func (m *Manager) Segment(id, segmentID string) ([]byte, error) {
	if _, ok := m.Get(id); !ok {
		return nil, fmt.Errorf("session not found")
	}
	return m.cache.Get(segmentID)
}

func (m *Manager) OpenSegment(id, segmentID string) (io.ReadCloser, int64, error) {
	if _, ok := m.Get(id); !ok {
		return nil, 0, fmt.Errorf("session not found")
	}
	f, size, err := m.cache.Open(segmentID)
	return f, size, err
}

func (m *Manager) ensureSegments(ctx context.Context, s *Session, urls []string) error {
	for _, u := range urls {
		s.mu.Lock()
		if _, ok := s.segMap[u]; ok {
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()
		if m.cache.Has(guessCached(u)) {
			// can't guess from URL — always fetch unless already mapped
		}
		data, _, err := m.fetch(ctx, u, s.UserAgent)
		if err != nil {
			return fmt.Errorf("segment fetch %s: %w", redact(u), err)
		}
		if int64(len(data)) > m.cfg.MaxSegmentBytes {
			return fmt.Errorf("segment too large")
		}
		sid, err := m.cache.Put(data)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.segMap[u] = sid
		s.mu.Unlock()
	}
	return nil
}

func (m *Manager) fetch(ctx context.Context, rawURL, ua string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Connection", "keep-alive")
	referer := ""
	originHdr := ""
	// Muitos painéis Xtream checam Referer/Origin no mesmo host.
	if u, err := url.Parse(rawURL); err == nil && u.Scheme != "" && u.Host != "" {
		referer = u.Scheme + "://" + u.Host + "/"
		originHdr = u.Scheme + "://" + u.Host
		req.Header.Set("Referer", referer)
		req.Header.Set("Origin", originHdr)
	}

	safeURL := redactURL(rawURL)
	log.Printf("origin GET url=%s ua=%q referer=%q origin=%q", safeURL, ua, referer, originHdr)
	start := time.Now()

	resp, err := m.client.Do(req)
	if err != nil {
		log.Printf("origin FAIL url=%s err=%v duration=%s", safeURL, err, time.Since(start))
		return nil, "", err
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// lê um trecho do body para diagnóstico (sem logar credenciais da URL)
		snip, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		log.Printf("origin RESP url=%s status=%d content-type=%q bytes~=%d body_snip=%q duration=%s",
			safeURL, resp.StatusCode, ct, len(snip), truncate(string(snip), 120), time.Since(start))
		return nil, "", &OriginError{Status: resp.StatusCode}
	}
	limited := io.LimitReader(resp.Body, m.cfg.MaxSegmentBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		log.Printf("origin READ url=%s err=%v duration=%s", safeURL, err, time.Since(start))
		return nil, "", err
	}
	if int64(len(data)) > m.cfg.MaxSegmentBytes {
		return nil, "", fmt.Errorf("response too large")
	}
	final := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	log.Printf("origin OK url=%s status=%d content-type=%q size=%d final=%s duration=%s",
		safeURL, resp.StatusCode, ct, len(data), redactURL(final), time.Since(start))
	return data, final, nil
}

// OriginError indica recusa HTTP da origem (ex.: 403 do painel Xtream).
type OriginError struct {
	Status int
}

func (e *OriginError) Error() string {
	return fmt.Sprintf("origin status %d", e.Status)
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func guessCached(_ string) string { return "" }

func redact(u string) string {
	return redactURL(u)
}

// redactURL mascara user/pass de paths Xtream e userinfo da URL.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return redactFallback(raw)
	}
	if u.User != nil {
		u.User = url.UserPassword("***", "***")
	}
	path := u.EscapedPath()
	// /live/user/pass/id.m3u8 → /live/***/***/id.m3u8
	u.Path = hls.RedactXtreamPath(path)
	u.RawPath = ""
	// query tokens
	q := u.Query()
	for _, key := range []string{"username", "password", "token", "auth", "key"} {
		if q.Has(key) {
			q.Set(key, "***")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func redactFallback(raw string) string {
	if len(raw) > 96 {
		return raw[:48] + "…[redacted]…" + raw[len(raw)-16:]
	}
	return raw
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
