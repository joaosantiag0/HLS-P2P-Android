package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr       string
	CacheDir         string
	CacheMaxBytes    int64
	SessionTTL       time.Duration
	MaxRedirects     int
	AllowPrivateNet  bool
	AccessSecret     string
	STUNURLs         []string
	TURNURLs         []string
	TURNUser         string
	TURNPass         string
	MeteredAPIKey    string
	MeteredAPIURL    string
	MaxSegmentBytes  int64
	OriginTimeout    time.Duration
	PlaylistPollMin  time.Duration
}

func Load() Config {
	// TURN público (Open Relay / Metered). Credenciais estáticas legadas;
	// para cota confiável, defina METERED_API_KEY (conta free em metered.ca).
	defaultTURN := strings.Join([]string{
		"turn:openrelay.metered.ca:80",
		"turn:openrelay.metered.ca:80?transport=tcp",
		"turn:openrelay.metered.ca:443",
		"turn:openrelay.metered.ca:443?transport=tcp",
	}, ",")

	turnCSV := getenv("TURN_URLS", "")
	if turnCSV == "" {
		turnCSV = getenv("TURN_URL", defaultTURN)
	}

	return Config{
		ListenAddr:      getenv("LISTEN_ADDR", ":8080"),
		CacheDir:        getenv("CACHE_DIR", "./data/cache"),
		CacheMaxBytes:   getenvInt64("CACHE_MAX_BYTES", 2<<30), // 2 GiB
		SessionTTL:      getenvDuration("SESSION_TTL", 2*time.Hour),
		MaxRedirects:    int(getenvInt64("MAX_REDIRECTS", 5)),
		AllowPrivateNet: getenv("ALLOW_PRIVATE_NET", "false") == "true",
		AccessSecret:    getenv("ACCESS_SECRET", ""),
		STUNURLs: splitCSV(getenv("STUN_URLS",
			"stun:stun.l.google.com:19302,stun:stun.relay.metered.ca:80")),
		TURNURLs:        splitCSV(turnCSV),
		TURNUser:        getenv("TURN_USER", "openrelayproject"),
		TURNPass:        getenv("TURN_PASS", "openrelayproject"),
		MeteredAPIKey:   getenv("METERED_API_KEY", ""),
		MeteredAPIURL:   getenv("METERED_API_URL", "https://openrelay.metered.ca/openrelayproject/turn/credentials"),
		MaxSegmentBytes: getenvInt64("MAX_SEGMENT_BYTES", 32<<20), // 32 MiB
		OriginTimeout:   getenvDuration("ORIGIN_TIMEOUT", 15*time.Second),
		PlaylistPollMin: getenvDuration("PLAYLIST_POLL_MIN", 2*time.Second),
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getenvInt64(k string, def int64) int64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func getenvDuration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
