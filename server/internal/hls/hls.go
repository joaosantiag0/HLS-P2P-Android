package hls

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	xtreamLive = regexp.MustCompile(`(?i)/(?:live|movie|series)/([^/]+)/([^/]+)/([^/]+?)(?:\.m3u8)?(?:\?|$)`)
	tagURI     = regexp.MustCompile(`URI="([^"]+)"`)
)

// StreamKey builds a stable swarm id without exposing credentials.
func StreamKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return opaqueKey(rawURL)
	}
	path := u.EscapedPath()
	if m := xtreamLive.FindStringSubmatch(path); len(m) == 4 {
		// m[1]=user m[2]=pass m[3]=streamId — omit credentials
		kind := "live"
		lower := strings.ToLower(path)
		switch {
		case strings.Contains(lower, "/movie/"):
			kind = "movie"
		case strings.Contains(lower, "/series/"):
			kind = "series"
		}
		host := strings.ToLower(u.Hostname())
		return fmt.Sprintf("%s|%s|%s|default", host, kind, m[3])
	}
	return opaqueKey(rawURL)
}

func opaqueKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "opaque:" + hex.EncodeToString(sum[:])
}

// URLKey is a stable swarm segment id from the absolute origin URL (not content hash).
func URLKey(absURL string) string {
	sum := sha256.Sum256([]byte(absURL))
	return hex.EncodeToString(sum[:])
}

var xtreamRedact = regexp.MustCompile(`(?i)/(live|movie|series)/[^/]+/[^/]+/`)

// RedactXtreamPath mascara user/pass em paths /live|movie|series/user/pass/...
func RedactXtreamPath(path string) string {
	return xtreamRedact.ReplaceAllString(path, "/$1/***/***/")
}

// RewritePlaylist rewrites media/master playlist URIs to proxied paths.
// segmentResolver maps absolute origin URL -> segmentId (already cached) or empty if media playlist URI.
// For #EXTINF segments, resolver must return segmentId.
// For playlist references (master variants), resolver returns "" and rewritePath is used.
type RewriteResult struct {
	Body      string
	IsMaster  bool
	TargetDur float64
}

func RewritePlaylist(body, baseURL, sessionID string, segmentIDs map[string]string) (RewriteResult, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return RewriteResult{}, err
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	isMaster := false
	var targetDur float64
	pendingSegment := false

	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#EXT-X-STREAM-INF") {
			isMaster = true
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trim, "#EXT-X-TARGETDURATION:") {
			fmt.Sscanf(trim, "#EXT-X-TARGETDURATION:%f", &targetDur)
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trim, "#EXTINF") {
			pendingSegment = true
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trim, "#") {
			// Rewrite URI= in tags (KEY, MAP, MEDIA)
			if strings.Contains(trim, "URI=\"") {
				out = append(out, rewriteTagURIs(line, base, sessionID, segmentIDs))
			} else {
				out = append(out, line)
			}
			continue
		}
		if trim == "" {
			out = append(out, line)
			continue
		}
		abs := resolve(base, trim)
		if pendingSegment || !isMaster {
			// ID estável por URL de origem — permite P2P entre sessões diferentes sem prefetch.
			key := URLKey(abs)
			out = append(out, fmt.Sprintf("/hls/%s/u/%s.ts", sessionID, key))
			pendingSegment = false
		} else {
			// Master variant playlist — rewrite to proxied playlist endpoint
			out = append(out, fmt.Sprintf("/hls/%s/variant?u=%s", sessionID, url.QueryEscape(abs)))
		}
	}
	return RewriteResult{Body: strings.Join(out, "\n"), IsMaster: isMaster, TargetDur: targetDur}, nil
}

func rewriteTagURIs(line string, base *url.URL, sessionID string, segmentIDs map[string]string) string {
	return tagURI.ReplaceAllStringFunc(line, func(m string) string {
		sub := tagURI.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		abs := resolve(base, sub[1])
		key := URLKey(abs)
		return fmt.Sprintf(`URI="/hls/%s/u/%s.ts"`, sessionID, key)
	})
}

func resolve(base *url.URL, ref string) string {
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(r).String()
}

// CollectURIs returns absolute URIs for segment lines and URI= attributes.
func CollectURIs(body, baseURL string) (segments []string, playlists []string, err error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	isMaster := false
	pending := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#EXT-X-STREAM-INF") {
			isMaster = true
			continue
		}
		if strings.HasPrefix(trim, "#EXTINF") {
			pending = true
			continue
		}
		if strings.HasPrefix(trim, "#") {
			if strings.Contains(trim, "URI=\"") {
				for _, sub := range tagURI.FindAllStringSubmatch(trim, -1) {
					if len(sub) >= 2 {
						segments = append(segments, resolve(base, sub[1]))
					}
				}
			}
			continue
		}
		if trim == "" {
			continue
		}
		abs := resolve(base, trim)
		if isMaster && !pending {
			playlists = append(playlists, abs)
		} else {
			segments = append(segments, abs)
			pending = false
		}
	}
	return segments, playlists, nil
}

// PickBestVariant chooses the last (usually highest) or first media playlist from master.
func PickBestVariant(playlists []string) string {
	if len(playlists) == 0 {
		return ""
	}
	// Prefer middle bitrate for TV boxes — use first for stability on low-end devices
	return playlists[0]
}
