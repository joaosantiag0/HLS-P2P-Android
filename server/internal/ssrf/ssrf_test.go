package ssrf_test

import (
	"testing"

	"github.com/p2pplayer/server/internal/ssrf"
)

func TestValidateURLRejectsNonHTTP(t *testing.T) {
	_, err := ssrf.ValidateURL("ftp://example.com/a", true)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateURLRejectsLocalhost(t *testing.T) {
	_, err := ssrf.ValidateURL("http://localhost/secret", false)
	if err == nil {
		t.Fatal("expected private block")
	}
}

func TestSanitizeUserAgent(t *testing.T) {
	ua := ssrf.SanitizeUserAgent("VLC/3.0\x00evil")
	if ua != "VLC/3.0evil" {
		t.Fatalf("got %q", ua)
	}
	if ssrf.SanitizeUserAgent("") != "HLS-P2P-Streaming-Android/1.0" {
		t.Fatal("default ua")
	}
}
