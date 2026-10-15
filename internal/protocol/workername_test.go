package protocol

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeWorkerName(t *testing.T) {
	long := strings.Repeat("\u015f", MaxWorkerNameRunes+10)
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"office-mac", "office-mac"},
		{"  living room pi \n", "living room pi"},
		{"   ", ""},
		{"G\u00f6khan's Mac mini", "G\u00f6khan's Mac mini"},
		{long, strings.Repeat("\u015f", MaxWorkerNameRunes)},
		{strings.Repeat("a", MaxWorkerNameRunes-1) + " b", strings.Repeat("a", MaxWorkerNameRunes-1)},
		{"two\nlines", ""},
		{"tab\there", ""},
		{"bell\a", ""},
		{"esc\x1b[31mred", ""},
		{"del\x7f", ""},
		{"c1\u0085x", ""},
		{"rtl\u202eoverride", ""},
		{"zero\u200bwidth", ""},
		{"line\u2028sep", ""},
		{"bad\xffutf8", ""},
	} {
		got := NormalizeWorkerName(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeWorkerName(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if n := utf8.RuneCountInString(got); n > MaxWorkerNameRunes {
			t.Errorf("NormalizeWorkerName(%q) has %d runes", tc.in, n)
		}
	}
}
