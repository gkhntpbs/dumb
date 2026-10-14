//go:build windows

package windowstray

import (
	"sync"
	"unsafe"

	"github.com/gkhntpbs/dumb/internal/branding"
)

var (
	createIcon = user.NewProc("CreateIconFromResourceEx")
	metrics    = user.NewProc("GetSystemMetrics")
	bearOnce   sync.Once
	bearIcon   uintptr
)

// trayIcon returns the DUMB bear sized for the notification area, falling back
// to the stock application icon if the embedded icon cannot be loaded.
func trayIcon() uintptr {
	bearOnce.Do(func() {
		size, _, _ := metrics.Call(49) // SM_CXSMICON
		if size == 0 {
			size = 16
		}
		data, _, err := icoEntry(branding.TrayICO, int(size))
		if err != nil {
			return
		}
		bearIcon, _, _ = createIcon.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 1, 0x00030000, size, size, 0)
	})
	if bearIcon != 0 {
		return bearIcon
	}
	image, _, _ := loadicon.Call(0, 32512)
	return image
}
