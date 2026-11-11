package wiki

import "testing"

func TestStoredContentType(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	gif := []byte("GIF89a\x01\x00\x01\x00")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	html := []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")
	cases := []struct {
		name, claimed string
		head          []byte
		want          string
	}{
		{"real png", "image/png", png, "image/png"},
		{"png with parameters", "image/png; charset=binary", png, "image/png"},
		{"misnamed raster", "image/jpeg", gif, "image/gif"},
		{"html claiming png", "image/png", html, "application/octet-stream"},
		{"svg claiming png", "image/png", svg, "application/octet-stream"},
		{"svg stays svg", "image/svg+xml", svg, "image/svg+xml"},
		{"html stays html", "text/html", html, "text/html"},
		{"empty type", "", []byte("plain"), "application/octet-stream"},
		{"empty type, png bytes", "", png, "image/png"},
		{"garbage type", "not a type;;", []byte("x"), "application/octet-stream"},
		{"pdf", "application/pdf", []byte("%PDF-1.7"), "application/pdf"},
	}
	for _, tc := range cases {
		if got := storedContentType(tc.claimed, tc.head); got != tc.want {
			t.Errorf("%s: storedContentType(%q) = %q, want %q", tc.name, tc.claimed, got, tc.want)
		}
	}
}

func TestInline(t *testing.T) {
	for _, ct := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		if !Inline(ct) {
			t.Errorf("%s should be inline", ct)
		}
	}
	for _, ct := range []string{"image/svg+xml", "text/html", "application/xhtml+xml", "application/pdf", "image/bmp", "application/octet-stream", ""} {
		if Inline(ct) {
			t.Errorf("%s must download as an attachment", ct)
		}
	}
}
