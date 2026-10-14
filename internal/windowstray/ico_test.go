package windowstray

import (
	"bytes"
	"testing"

	"github.com/gkhntpbs/dumb/internal/branding"
)

func TestIcoEntryPicksTraySizes(t *testing.T) {
	for _, c := range []struct{ want, got int }{{16, 16}, {20, 20}, {24, 24}, {32, 32}, {40, 48}, {64, 64}, {100, 256}, {512, 256}} {
		b, s, err := icoEntry(branding.TrayICO, c.want)
		if err != nil || s != c.got || !bytes.HasPrefix(b, []byte("\x89PNG")) {
			t.Fatalf("size %d: got %d, err %v", c.want, s, err)
		}
	}
	if _, _, err := icoEntry([]byte("nope"), 16); err == nil {
		t.Fatal("accepted garbage")
	}
}
