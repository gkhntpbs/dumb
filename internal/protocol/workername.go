package protocol

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxWorkerNameRunes bounds a worker's display name.
const MaxWorkerNameRunes = 64

// NormalizeWorkerName returns the display form of a worker name: surrounding
// whitespace trimmed and at most MaxWorkerNameRunes runes kept. A name with
// invalid UTF-8, inner control characters (such as newlines and tabs) or format
// characters (such as bidirectional overrides) or line/paragraph separators yields "" so that it is treated
// as absent. The name is display-only and never an identifier.
func NormalizeWorkerName(s string) string {
	if !utf8.ValidString(s) {
		return ""
	}
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ""
		}
	}
	if r := []rune(s); len(r) > MaxWorkerNameRunes {
		s = strings.TrimSpace(string(r[:MaxWorkerNameRunes]))
	}
	return s
}
