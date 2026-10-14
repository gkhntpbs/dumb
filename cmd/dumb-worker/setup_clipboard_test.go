package main

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func clipboardBytes(text string, terminated bool) []byte {
	words := utf16.Encode([]rune(text))
	if terminated {
		words = append(words, 0)
	}
	out := make([]byte, len(words)*2)
	for i, w := range words {
		binary.LittleEndian.PutUint16(out[i*2:], w)
	}
	return out
}
func TestSetupClipboardBoundedUTF16(t *testing.T) {
	for _, text := range []string{"text", "  text  ", "unicode🐻"} {
		got, err := decodeSetupClipboard(clipboardBytes(text, true))
		if err != nil || got != strings.TrimSpace(text) {
			t.Fatalf("valid clipboard failed: %q %v", got, err)
		}
	}
	for _, data := range [][]byte{nil, {1}, {1, 2, 3}, clipboardBytes("unterminated", false), clipboardBytes("first\nsecond", true), clipboardBytes("first\rsecond", true), make([]byte, 65538)} {
		if _, err := decodeSetupClipboard(data); err == nil {
			t.Fatal("malformed or multiline clipboard accepted")
		}
	}
}
