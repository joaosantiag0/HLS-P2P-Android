package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

func (s *Server) iceServers() []iceServer {
	if s.cfg.MeteredAPIKey != "" {
		if cached := s.fetchMeteredICE(); len(cached) > 0 {
			return cached
		}
	}
	return s.staticICEServers()
}

func (s *Server) staticICEServers() []iceServer {
	out := make([]iceServer, 0, 4)
	if len(s.cfg.STUNURLs) > 0 {
		out = append(out, iceServer{URLs: s.cfg.STUNURLs})
	}
	if len(s.cfg.TURNURLs) > 0 {
		out = append(out, iceServer{
			URLs:       append([]string(nil), s.cfg.TURNURLs...),
			Username:   s.cfg.TURNUser,
			Credential: s.cfg.TURNPass,
		})
	}
	return out
}

func (s *Server) fetchMeteredICE() []iceServer {
	s.iceMu.Lock()
	if time.Now().Before(s.iceUntil) && len(s.iceCached) > 0 {
		out := append([]iceServer(nil), s.iceCached...)
		s.iceMu.Unlock()
		return out
	}
	s.iceMu.Unlock()

	endpoint := s.cfg.MeteredAPIURL
	u, err := url.Parse(endpoint)
	if err != nil {
		log.Printf("metered turn: bad url: %v", err)
		return nil
	}
	q := u.Query()
	q.Set("apiKey", s.cfg.MeteredAPIKey)
	u.RawQuery = q.Encode()

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		log.Printf("metered turn fetch: %v", err)
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode >= 300 {
		log.Printf("metered turn status=%d body=%s", resp.StatusCode, string(body))
		return nil
	}

	var raw []map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		log.Printf("metered turn json: %v", err)
		return nil
	}
	out := make([]iceServer, 0, len(raw))
	for _, item := range raw {
		srv := iceServer{
			Username:   strField(item, "username"),
			Credential: strField(item, "credential"),
		}
		switch v := item["urls"].(type) {
		case string:
			srv.URLs = []string{v}
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					srv.URLs = append(srv.URLs, s)
				}
			}
		}
		if len(srv.URLs) > 0 {
			out = append(out, srv)
		}
	}
	if len(out) == 0 {
		return nil
	}

	s.iceMu.Lock()
	s.iceCached = out
	s.iceUntil = time.Now().Add(5 * time.Minute)
	s.iceMu.Unlock()
	log.Printf("metered turn: loaded %d ice servers", len(out))
	return append([]iceServer(nil), out...)
}

func strField(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
