//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var consoleKernel = windows.NewLazySystemDLL("kernel32.dll")
var consoleUser = windows.NewLazySystemDLL("user32.dll")

type setupInputRecord struct {
	Kind    uint16
	Padding uint16
	Data    [16]byte
}
type setupKeyEvent struct {
	Down      int32
	Repeat    uint16
	Virtual   uint16
	Scan      uint16
	Character uint16
	State     uint32
}

func setupClipboardText() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	opened, _, err := consoleUser.NewProc("OpenClipboard").Call(0)
	if opened == 0 {
		return "", fmt.Errorf("Clipboard is busy. Try Ctrl+V again: %w", err)
	}
	defer consoleUser.NewProc("CloseClipboard").Call()
	handle, _, _ := consoleUser.NewProc("GetClipboardData").Call(13)
	if handle == 0 {
		return "", errors.New("Copy text to the clipboard first, then press Ctrl+V")
	}
	size, _, _ := consoleKernel.NewProc("GlobalSize").Call(handle)
	if size < 2 || size > 65536 {
		return "", errors.New("Clipboard text is too long; paste one field only")
	}
	pointer, _, _ := consoleKernel.NewProc("GlobalLock").Call(handle)
	if pointer == 0 {
		return "", errors.New("Clipboard text could not be read; try again")
	}
	defer consoleKernel.NewProc("GlobalUnlock").Call(handle)
	// GlobalLock returns a native address. Copy through the typed API while
	// locked, avoiding a uintptr-to-Go-pointer conversion rejected by vet.
	data := make([]byte, int(size))
	var copied uintptr
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), pointer, &data[0], size, &copied); err != nil || copied != size {
		return "", errors.New("Clipboard text could not be copied; try again")
	}
	return decodeSetupClipboard(data)
}

func setupNativeRead(input *os.File, output io.Writer, label string, secret bool) (string, error) {
	handle := windows.Handle(input.Fd())
	var original uint32
	if err := windows.GetConsoleMode(handle, &original); err != nil {
		return "", err
	}
	mode := original | windows.ENABLE_EXTENDED_FLAGS | windows.ENABLE_QUICK_EDIT_MODE | windows.ENABLE_INSERT_MODE
	mode &^= windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT
	if err := windows.SetConsoleMode(handle, mode); err != nil {
		return "", err
	}
	defer windows.SetConsoleMode(handle, original)
	fmt.Fprint(output, label+": ")
	var line []uint16
	appendText := func(text []uint16) {
		available := 4096 - len(line)
		if len(text) > available {
			fmt.Fprint(output, "\a")
			return
		}
		line = append(line, text...)
		if secret {
			fmt.Fprint(output, strings.Repeat("*", len(text)))
		} else {
			fmt.Fprint(output, string(utf16.Decode(text)))
		}
	}
	for {
		var record setupInputRecord
		var count uint32
		ok, _, err := consoleKernel.NewProc("ReadConsoleInputW").Call(uintptr(handle), uintptr(unsafe.Pointer(&record)), 1, uintptr(unsafe.Pointer(&count)))
		if ok == 0 {
			return "", fmt.Errorf("console input failed: %w", err)
		}
		if record.Kind != 1 || count != 1 {
			continue
		}
		key := *(*setupKeyEvent)(unsafe.Pointer(&record.Data[0]))
		if key.Down == 0 {
			continue
		}
		if key.Virtual == 0x56 && key.State&(0x4|0x8) != 0 {
			text, err := setupClipboardText()
			if err != nil {
				fmt.Fprintf(output, "\n%v\n%s: ", err, label)
				line = nil
				continue
			}
			appendText(utf16.Encode([]rune(text)))
			continue
		}
		if key.Virtual == 13 || key.Character == '\r' {
			fmt.Fprintln(output)
			if secret && len(line) > 0 {
				fmt.Fprintln(output, "Token entered; its contents are hidden.")
			}
			return string(utf16.Decode(line)), nil
		}
		if key.Virtual == 8 || key.Character == '\b' {
			if len(line) > 0 {
				line = line[:len(line)-1]
				fmt.Fprint(output, "\b \b")
			}
			continue
		}
		if key.Character < 32 || key.Character == 127 {
			continue
		}
		for i := uint16(0); i < key.Repeat; i++ {
			appendText([]uint16{key.Character})
		}
	}
}

func setupConsoleColumns(output *os.File) int {
	type coord struct{ X, Y int16 }
	type rect struct{ Left, Top, Right, Bottom int16 }
	type info struct {
		Size, Cursor coord
		Attributes   uint16
		Window       rect
		Maximum      coord
	}
	var current info
	ok, _, _ := consoleKernel.NewProc("GetConsoleScreenBufferInfo").Call(output.Fd(), uintptr(unsafe.Pointer(&current)))
	if ok != 0 {
		return int(current.Window.Right - current.Window.Left + 1)
	}
	return 80
}
