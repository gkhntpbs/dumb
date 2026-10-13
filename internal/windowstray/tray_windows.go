//go:build windows

package windowstray

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"sync"
	"unsafe"

	"github.com/gkhntpbs/dumb/internal/menubar"
	"golang.org/x/sys/windows"
)

var (
	user           = windows.NewLazySystemDLL("user32.dll")
	shell          = windows.NewLazySystemDLL("shell32.dll")
	register       = user.NewProc("RegisterClassExW")
	create         = user.NewProc("CreateWindowExW")
	defproc        = user.NewProc("DefWindowProcW")
	getmsg         = user.NewProc("GetMessageW")
	dispatch       = user.NewProc("DispatchMessageW")
	translate      = user.NewProc("TranslateMessage")
	post           = user.NewProc("PostMessageW")
	destroy        = user.NewProc("DestroyWindow")
	quit           = user.NewProc("PostQuitMessage")
	loadicon       = user.NewProc("LoadIconW")
	notify         = shell.NewProc("Shell_NotifyIconW")
	popup          = user.NewProc("CreatePopupMenu")
	appendmenu     = user.NewProc("AppendMenuW")
	track          = user.NewProc("TrackPopupMenu")
	destroymenu    = user.NewProc("DestroyMenu")
	cursor         = user.NewProc("GetCursorPos")
	foreground     = user.NewProc("SetForegroundWindow")
	regmessage     = user.NewProc("RegisterWindowMessageW")
	mu             sync.Mutex
	status         = menubar.Status{Version: 1, State: "starting", Category: "none"}
	taskbarMessage uint32
	input          io.Reader
	output         io.Writer
	window         uintptr
	outputErr      error
)

const (
	wmApp         = 0x8000
	trayMessage   = wmApp + 1
	statusMessage = wmApp + 2
	wmClose       = 0x10
	wmDestroy     = 2
)

type point struct{ X, Y int32 }
type message struct {
	Hwnd           uintptr
	Message        uint32
	Wparam, Lparam uintptr
	Time           uint32
	Point          point
	Private        uint32
}
type wndclass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}
type notifyicon struct {
	Size                uint32
	Hwnd                uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                windows.GUID
	BalloonIcon         uintptr
}

func utf(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }
func emit(event string) {
	if err := json.NewEncoder(output).Encode(menubar.Event{Version: 1, Event: event}); err != nil {
		outputErr = err
		post.Call(window, wmClose, 0, 0)
	}
}
func icon(hwnd uintptr, operation uintptr) bool {
	mu.Lock()
	current := status
	mu.Unlock()
	image, _, _ := loadicon.Call(0, 32512)
	n := notifyicon{Hwnd: hwnd, ID: 1, Flags: 1 | 2 | 4, Callback: trayMessage, Icon: image}
	n.Size = uint32(unsafe.Sizeof(n))
	text := windows.StringToUTF16(fmt.Sprintf("DUMB: %s | %d active | %s", current.State, current.Active, current.Category))
	copy(n.Tip[:], text)
	ok, _, _ := notify.Call(operation, uintptr(unsafe.Pointer(&n)))
	return ok != 0
}
func menu(hwnd uintptr) {
	mu.Lock()
	current := status
	mu.Unlock()
	m, _, _ := popup.Call()
	if m == 0 {
		return
	}
	defer destroymenu.Call(m)
	appendmenu.Call(m, 2, 0, uintptr(unsafe.Pointer(utf(fmt.Sprintf("DUMB: %s (%d active)", current.State, current.Active)))))
	action, label, id := "pause", "Pause", uintptr(1)
	if current.State == "paused" {
		action, label, id = "resume", "Resume", 2
	}
	flags := uintptr(0)
	if current.State == "draining" || current.State == "stopping" {
		flags = 2
	}
	appendmenu.Call(m, flags, id, uintptr(unsafe.Pointer(utf(label))))
	appendmenu.Call(m, 0, 3, uintptr(unsafe.Pointer(utf("Quit"))))
	var p point
	cursor.Call(uintptr(unsafe.Pointer(&p)))
	foreground.Call(hwnd)
	selected, _, _ := track.Call(m, 0x100|2, uintptr(p.X), uintptr(p.Y), 0, hwnd, 0)
	switch selected {
	case id:
		emit(action)
	case 3:
		emit("quit")
	}
	post.Call(hwnd, 0, 0, 0)
}
func windowproc(hwnd uintptr, msg uint32, w, l uintptr) uintptr {
	switch msg {
	case trayMessage:
		if uint32(l) == 0x205 || uint32(l) == 0x202 || uint32(l) == 0x7b {
			menu(hwnd)
		}
		return 0
	case statusMessage:
		if !icon(hwnd, 1) {
			destroy.Call(hwnd)
		}
		return 0
	case wmClose:
		destroy.Call(hwnd)
		return 0
	case wmDestroy:
		icon(hwnd, 2)
		quit.Call(0)
		return 0
	}
	if msg == taskbarMessage {
		if !icon(hwnd, 0) {
			destroy.Call(hwnd)
		}
		return 0
	}
	r, _, _ := defproc.Call(hwnd, uintptr(msg), w, l)
	return r
}
func Run(reader io.Reader, writer io.Writer) error {
	input, output = reader, writer

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	name := utf("DUMBTrayWindow")
	wc := wndclass{Proc: windows.NewCallback(windowproc), ClassName: name}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, _ := register.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("native tray initialization failed")
	}
	hwnd, _, _ := create.Call(0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if hwnd == 0 {
		return fmt.Errorf("native tray initialization failed")
	}
	r, _, _ := regmessage.Call(uintptr(unsafe.Pointer(utf("TaskbarCreated"))))
	taskbarMessage = uint32(r)
	if !icon(hwnd, 0) {
		destroy.Call(hwnd)
		return fmt.Errorf("native tray initialization failed")
	}
	window = hwnd
	emit("ready")
	go func() {
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 1024), 4096)
		for scanner.Scan() {
			var next menubar.Status
			if json.Unmarshal(scanner.Bytes(), &next) != nil || !next.Valid() {
				break
			}
			mu.Lock()
			status = next
			mu.Unlock()
			post.Call(hwnd, statusMessage, 0, 0)
		}
		post.Call(hwnd, wmClose, 0, 0)
	}()
	var msg message
	for {
		r, _, _ := getmsg.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 {
			break
		}
		if int32(r) == -1 {
			return fmt.Errorf("native tray initialization failed")
		}
		translate.Call(uintptr(unsafe.Pointer(&msg)))
		dispatch.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return outputErr
}
