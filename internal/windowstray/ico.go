package windowstray

import (
	"encoding/binary"
	"errors"
)

// icoEntry returns the image bytes of the smallest entry in an .ico file that
// is at least size pixels wide, or the largest entry when none is.
func icoEntry(data []byte, size int) ([]byte, int, error) {
	if len(data) < 6 || binary.LittleEndian.Uint16(data[2:]) != 1 {
		return nil, 0, errors.New("not an icon file")
	}
	count := int(binary.LittleEndian.Uint16(data[4:]))
	best, bestSize := -1, 0
	for i := 0; i < count; i++ {
		h := 6 + 16*i
		if h+16 > len(data) {
			return nil, 0, errors.New("truncated icon directory")
		}
		s := int(data[h])
		if s == 0 {
			s = 256
		}
		better := best < 0 ||
			(s >= size && (bestSize < size || s < bestSize)) ||
			(s < size && bestSize < size && s > bestSize)
		if better {
			best, bestSize = h, s
		}
	}
	if best < 0 {
		return nil, 0, errors.New("empty icon file")
	}
	n := int(binary.LittleEndian.Uint32(data[best+8:]))
	off := int(binary.LittleEndian.Uint32(data[best+12:]))
	if off < 0 || n <= 0 || off+n > len(data) {
		return nil, 0, errors.New("icon entry out of range")
	}
	return data[off : off+n], bestSize, nil
}
