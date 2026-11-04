package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	user32         = syscall.NewLazyDLL("user32.dll")
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procKeybdEvent = user32.NewProc("keybd_event")
	procSendInput  = user32.NewProc("SendInput")
	procLock       = user32.NewProc("LockWorkStation")
	procSendMsg    = user32.NewProc("SendMessageW")
	procEnumWin    = user32.NewProc("EnumWindows")
	procGetText    = user32.NewProc("GetWindowTextW")
	procVisible    = user32.NewProc("IsWindowVisible")
	procWinPid     = user32.NewProc("GetWindowThreadProcessId")
	procSetFg      = user32.NewProc("SetForegroundWindow")
	procShowWin    = user32.NewProc("ShowWindow")
	procIconic     = user32.NewProc("IsIconic")
	procOpenProc   = kernel32.NewProc("OpenProcess")
	procImageName  = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseH     = kernel32.NewProc("CloseHandle")
)

const (
	vkVolumeMute     = 0xAD
	vkVolumeDown     = 0xAE
	vkVolumeUp       = 0xAF
	vkMediaNext      = 0xB0
	vkMediaPrev      = 0xB1
	vkMediaPlayPause = 0xB3
	keyeventfKeyup   = 0x0002
)

func defaultButtons() []Button {
	return append(commonButtons(),
		Button{ID: "browser", Label: "ブラウザ", Icon: "🌐", Color: "#8b5cf6", Open: "https://www.google.com"},
		Button{ID: "files", Label: "エクスプローラー", Icon: "📁", Color: "#8b5cf6", Open: "~"},
		Button{ID: "terminal", Label: "ターミナル", Icon: "💻", Color: "#8b5cf6", Cmd: "start wt || start cmd"},
		Button{ID: "lock", Label: "画面ロック", Icon: "🔒", Color: "#64748b", Action: "screen.lock"},
		Button{ID: "off", Label: "画面オフ", Icon: "🌙", Color: "#64748b", Action: "screen.off"},
		Button{ID: "settings", Label: "設定", Icon: "⚙️", Color: "#64748b", Open: "ms-settings:"},
	)
}

// cmd.exe は独自のクォート規則なので、引数をそのまま渡す
func shellCommand(s string) *exec.Cmd {
	cmd := exec.Command("cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "/C " + s}
	return cmd
}

func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags = 0x00000200 // CREATE_NEW_PROCESS_GROUP
}

func openTarget(t string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", t).Start()
}

// Windows はメディアキー・音量キーを押したことにするのが一番確実
func pressKey(vk uintptr) error {
	procKeybdEvent.Call(vk, 0, 0, 0)
	procKeybdEvent.Call(vk, 0, keyeventfKeyup, 0)
	return nil
}

func media(k mediaKey) error {
	return pressKey([...]uintptr{vkMediaPlayPause, vkMediaNext, vkMediaPrev}[k])
}

// 音量キー 1 回で 2% 変わるので、5% 相当として 2〜3 回押す
func volume(delta int) error {
	vk := uintptr(vkVolumeUp)
	if delta < 0 {
		vk = vkVolumeDown
	}
	for i := 0; i < (abs(delta)+1)/2; i++ {
		pressKey(vk)
	}
	return nil
}

func toggleMute() error { return pressKey(vkVolumeMute) }

// 音量の取得には COM (IAudioEndpointVolume) が必要なので未対応。画面上部に表示されないだけ
func getAudio() *Audio { return nil }

func lockScreen() error {
	if r, _, err := procLock.Call(); r == 0 {
		return err
	}
	return nil
}

func screenOff() error {
	const hwndBroadcast, wmSyscommand, scMonitorpower = 0xFFFF, 0x0112, 0xF170
	// SendMessage は応答待ちで固まることがあるので非同期で投げる
	go procSendMsg.Call(hwndBroadcast, wmSyscommand, scMonitorpower, 2)
	return nil
}

// --- ウィンドウを探す ---

type window struct {
	hwnd  uintptr
	title string
	exe   string // 実行ファイル名（小文字）
}

var (
	enumMu    sync.Mutex
	enumFound []window
	// コールバックは作れる数に上限があるので 1 つだけ作って使い回す
	enumCb = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		if v, _, _ := procVisible.Call(hwnd); v == 0 {
			return 1
		}
		buf := make([]uint16, 512)
		n, _, _ := procGetText.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n == 0 {
			return 1
		}
		enumFound = append(enumFound, window{hwnd: hwnd, title: syscall.UTF16ToString(buf[:n]), exe: windowExe(hwnd)})
		return 1
	})
)

func windowExe(hwnd uintptr) string {
	var pid uint32
	procWinPid.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	const processQueryLimitedInformation = 0x1000
	h, _, _ := procOpenProc.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseH.Call(h)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	if r, _, _ := procImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:size])))
}

func listWindows() []window {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumFound = nil
	procEnumWin.Call(enumCb, 0)
	return enumFound
}

// タイトルに focus を含むウィンドウを前面に出す
func focusWindow(focus string) error {
	for _, w := range listWindows() {
		if !strings.Contains(strings.ToLower(w.title), strings.ToLower(focus)) {
			continue
		}
		if r, _, _ := procIconic.Call(w.hwnd); r != 0 {
			const swRestore = 9
			procShowWin.Call(w.hwnd, swRestore)
		}
		// Alt を押してから呼ぶと、別のアプリからでも前面に出せる
		pressKey(0x12)
		procSetFg.Call(w.hwnd)
		time.Sleep(150 * time.Millisecond)
		return nil
	}
	return fmt.Errorf("「%s」のウィンドウが見つかりません", focus)
}

// --- マイク: Windows は COM が必要なので未対応 ---

var errNoMic = errors.New("Windows ではマイクのミュートに未対応です（アプリ側のショートカットを keys で使ってください）")

func toggleMic() error        { return errNoMic }
func micMuted() (bool, error) { return false, errNoMic }

// --- Spotify: 操作はメディアキー、曲名はウィンドウタイトル（「アーティスト - 曲名」）から取る ---

func spotify(k mediaKey) error { return media(k) }

type track struct {
	Title   string
	Playing bool
}

func spotifyTrack() (track, error) {
	for _, w := range listWindows() {
		if w.exe != "spotify.exe" {
			continue
		}
		// 止まっているときのタイトルは「Spotify」「Spotify Premium」など
		if strings.HasPrefix(w.title, "Spotify") || !strings.Contains(w.title, " - ") {
			continue
		}
		return track{Title: w.title, Playing: true}, nil
	}
	return track{}, nil
}

// --- キー操作 ---

var winKeyCodes = func() map[string]uintptr {
	m := map[string]uintptr{
		"space": 0x20, "enter": 0x0D, "esc": 0x1B, "tab": 0x09, "backspace": 0x08, "delete": 0x2E,
		"up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27, "home": 0x24, "end": 0x23,
		"pageup": 0x21, "pagedown": 0x22, "minus": 0xBD, "equal": 0xBB, "comma": 0xBC,
		"period": 0xBE, "slash": 0xBF, "printscreen": 0x2C,
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = uintptr(c - 'a' + 'A')
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = uintptr(c)
	}
	for i := 1; i <= 12; i++ {
		m[fmt.Sprintf("f%d", i)] = uintptr(0x6F + i)
	}
	return m
}()

// 矢印キーなどは「拡張キー」として送らないとテンキー扱いになる
var winExtended = map[string]bool{"up": true, "down": true, "left": true, "right": true,
	"home": true, "end": true, "pageup": true, "pagedown": true, "delete": true}

// SendInput に渡す INPUT 構造体（キーボードの分だけ。後ろの余白は MOUSEINPUT と同じ大きさにするため）
type keybdInput struct {
	typ uint32
	ki  struct {
		vk, scan    uint16
		flags, time uint32
		extra       uintptr
	}
	_ [8]byte
}

// 文字の入力。KEYEVENTF_UNICODE で文字そのものを送るので、日本語入力の状態に関係なく日本語も打てる
func typeText(s string, enter bool, focus string) error {
	if focus != "" {
		if err := focusWindow(focus); err != nil {
			return err
		}
	}
	const inputKeyboard, keyeventfUnicode = 1, 0x0004
	var in []keybdInput
	for _, c := range utf16.Encode([]rune(strings.ReplaceAll(s, "\r\n", "\n"))) {
		if c == '\n' {
			c = '\r'
		}
		for _, up := range []uint32{0, keyeventfKeyup} {
			var k keybdInput
			k.typ = inputKeyboard
			k.ki.scan = c
			k.ki.flags = keyeventfUnicode | up
			in = append(in, k)
		}
	}
	if len(in) > 0 {
		n, _, err := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
		if int(n) != len(in) {
			return fmt.Errorf("文字を入力できませんでした: %v", err)
		}
	}
	if enter {
		return sendKeys(keyCombo{Key: "enter"}, "")
	}
	return nil
}

func sendKeys(k keyCombo, focus string) error {
	if focus != "" {
		if err := focusWindow(focus); err != nil {
			return err
		}
	}
	var mods []uintptr
	for _, m := range []struct {
		on bool
		vk uintptr
	}{{k.Ctrl, 0x11}, {k.Shift, 0x10}, {k.Alt, 0x12}, {k.Cmd, 0x5B}} {
		if m.on {
			mods = append(mods, m.vk)
		}
	}
	const keyeventfExtended = 0x0001
	flags := uintptr(0)
	if winExtended[k.Key] {
		flags = keyeventfExtended
	}
	vk := winKeyCodes[k.Key]
	for _, m := range mods {
		procKeybdEvent.Call(m, 0, 0, 0)
	}
	procKeybdEvent.Call(vk, 0, flags, 0)
	procKeybdEvent.Call(vk, 0, flags|keyeventfKeyup, 0)
	for i := len(mods) - 1; i >= 0; i-- {
		procKeybdEvent.Call(mods[i], 0, keyeventfKeyup, 0)
	}
	return nil
}
