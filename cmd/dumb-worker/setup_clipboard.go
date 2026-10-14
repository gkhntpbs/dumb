package main

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"
)

// decodeSetupClipboard reads a bounded copy, never a retained foreign pointer.
// CF_UNICODETEXT is NUL-terminated UTF-16LE; allocation padding after the first
// terminator is ignored. Native clipboard locking remains Windows-only.
func decodeSetupClipboard(data []byte) (string, error) {
	if len(data) < 2 || len(data) > 65536 || len(data)%2 != 0 {
		return "", errors.New("Clipboard text is too long or malformed; paste one field only")
	}
	words := make([]uint16, 0, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		word := binary.LittleEndian.Uint16(data[i:])
		if word == 0 {
			text := strings.TrimSpace(string(utf16.Decode(words)))
			if strings.ContainsAny(text, "\r\n") {
				return "", errors.New("Paste one line only")
			}
			return text, nil
		}
		words = append(words, word)
	}
	return "", errors.New("Clipboard text is not terminated; paste one field only")
}
