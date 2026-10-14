package branding

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogos(t *testing.T) {
	if !strings.Contains(Logo, "Distributed User-owned Model Backend") || !strings.Contains(Logo, "|____/") {
		t.Fatal("full logo is missing the wordmark")
	}
	if strings.Count(LogoCompact, "\n") < 6 || strings.Contains(LogoCompact, "Distributed") {
		t.Fatal("compact logo is not the small bear")
	}
}

func TestTrayICOSizes(t *testing.T) {
	d := TrayICO
	if binary.LittleEndian.Uint16(d[2:]) != 1 {
		t.Fatal("not an icon file")
	}
	var got []int
	for i := 0; i < int(binary.LittleEndian.Uint16(d[4:])); i++ {
		s := int(d[6+16*i])
		if s == 0 {
			s = 256
		}
		got = append(got, s)
	}
	want := []int{16, 20, 24, 32, 48, 64, 256}
	if len(got) != len(want) {
		t.Fatalf("sizes %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sizes %v, want %v", got, want)
		}
	}
}

func TestRegister(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)
	for path, ct := range map[string]string{"/favicon.ico": "image/x-icon", "/icon.png": "image/png", "/apple-touch-icon.png": "image/png"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || rec.Header().Get("Content-Type") != ct || rec.Body.Len() == 0 {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
