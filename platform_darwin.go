package main

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

func defaultButtons() []Button {
	return append(commonButtons(),
		Button{ID: "browser", Label: "ブラウザ", Icon: "🌐", Color: "#8b5cf6", Open: "https://www.google.com"},
		Button{ID: "files", Label: "Finder", Icon: "📁", Color: "#8b5cf6", Open: "~"},
		Button{ID: "terminal", Label: "ターミナル", Icon: "💻", Color: "#8b5cf6", Cmd: "open -a Terminal"},
		Button{ID: "lock", Label: "画面ロック", Icon: "🔒", Color: "#64748b", Action: "screen.lock"},
		Button{ID: "off", Label: "画面オフ", Icon: "🌙", Color: "#64748b", Action: "screen.off"},
		Button{ID: "settings", Label: "設定", Icon: "⚙️", Color: "#64748b", Cmd: "open -b com.apple.systempreferences"},
	)
}

func shellCommand(s string) *exec.Cmd { return exec.Command("sh", "-c", s) }

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func openTarget(t string) error { return exec.Command("open", t).Start() }

// メディアキーを押したことにする (NX_KEYTYPE_PLAY=16, NEXT=17, PREVIOUS=18)。
// 初回は「アクセシビリティ」の許可を求められる
const mediaKeyJXA = `
ObjC.import('Cocoa');
[true, false].forEach(function (down) {
  var ev = $.NSEvent.otherEventWithTypeLocationModifierFlagsTimestampWindowNumberContextSubtypeData1Data2(
    14, $.NSMakePoint(0, 0), down ? 0xa00 : 0xb00, 0, 0, null, 8, (%d << 16) | ((down ? 0xa : 0xb) << 8), -1);
  $.CGEventPost(0, ev.CGEvent);
});`

func media(k mediaKey) error {
	code := [...]int{16, 17, 18}[k]
	_, err := execOutput("osascript", "-l", "JavaScript", "-e", fmt.Sprintf(mediaKeyJXA, code))
	return err
}

func volume(delta int) error {
	_, err := execOutput("osascript", "-e",
		fmt.Sprintf("set volume output volume ((output volume of (get volume settings)) + %d)", delta))
	return err
}

func toggleMute() error {
	_, err := execOutput("osascript", "-e", "set volume output muted (not (output muted of (get volume settings)))")
	return err
}

var (
	macVol   = regexp.MustCompile(`output volume:(\d+)`)
	macMuted = regexp.MustCompile(`output muted:true`)
)

func getAudio() *Audio {
	out, err := execOutput("osascript", "-e", "get volume settings")
	m := macVol.FindStringSubmatch(out)
	if err != nil || m == nil {
		return nil
	}
	v, _ := strconv.Atoi(m[1])
	return &Audio{Volume: v, Muted: macMuted.MatchString(out)}
}

func lockScreen() error {
	_, err := execOutput("osascript", "-e",
		`tell application "System Events" to keystroke "q" using {control down, command down}`)
	return err
}

func screenOff() error {
	_, err := execOutput("pmset", "displaysleepnow")
	return err
}

// --- マイク: Mac にはマイクのミュートがないので、入力音量を 0 にして戻す ---

var (
	micMu   sync.Mutex
	micLast = 70 // ミュートを解除したときに戻す音量
)

func inputVolume() (int, error) {
	out, err := execOutput("osascript", "-e", "input volume of (get volume settings)")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

func toggleMic() error {
	micMu.Lock()
	defer micMu.Unlock()
	v, err := inputVolume()
	if err != nil {
		return err
	}
	next := 0
	if v == 0 {
		next = micLast
	} else {
		micLast = v
	}
	_, err = execOutput("osascript", "-e", fmt.Sprintf("set volume input volume %d", next))
	return err
}

func micMuted() (bool, error) {
	v, err := inputVolume()
	return v == 0, err
}

// --- Spotify: AppleScript で Spotify を名指しで操作する ---

func spotify(k mediaKey) error {
	cmd := [...]string{"playpause", "next track", "previous track"}[k]
	out, err := execOutput("osascript", "-e", `if application "Spotify" is running then
	tell application "Spotify" to `+cmd+`
	return "ok"
end if`)
	if err != nil {
		return err
	}
	if out != "ok" {
		return errors.New("Spotify が起動していません")
	}
	return nil
}

type track struct {
	Title   string
	Playing bool
}

func spotifyTrack() (track, error) {
	out, err := execOutput("osascript", "-e", `if application "Spotify" is running then
	tell application "Spotify"
		if player state is stopped then return ""
		return (player state as string) & linefeed & (artist of current track) & " - " & (name of current track)
	end tell
end if
return ""`)
	if err != nil || out == "" {
		return track{}, nil
	}
	state, title, _ := strings.Cut(out, "\n")
	return track{Title: title, Playing: state == "playing"}, nil
}

// --- キー操作: System Events で押す（初回は「アクセシビリティ」の許可が必要） ---

var macKeyCodes = map[string]int{
	"space": 49, "enter": 36, "esc": 53, "tab": 48, "backspace": 51, "delete": 117,
	"up": 126, "down": 125, "left": 123, "right": 124, "home": 115, "end": 119,
	"pageup": 116, "pagedown": 121, "f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96,
	"f6": 97, "f7": 98, "f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111,
}

var macChars = map[string]string{"minus": "-", "equal": "=", "comma": ",", "period": ".", "slash": "/"}

func appleString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func sendKeys(k keyCombo, focus string) error {
	var mods []string
	for _, m := range []struct {
		on   bool
		name string
	}{{k.Cmd, "command down"}, {k.Ctrl, "control down"}, {k.Alt, "option down"}, {k.Shift, "shift down"}} {
		if m.on {
			mods = append(mods, m.name)
		}
	}
	press := ""
	if code, ok := macKeyCodes[k.Key]; ok {
		press = fmt.Sprintf("key code %d", code)
	} else if c, ok := macChars[k.Key]; ok {
		press = "keystroke " + appleString(c)
	} else {
		press = "keystroke " + appleString(k.Key)
	}
	if len(mods) > 0 {
		press += " using {" + strings.Join(mods, ", ") + "}"
	}
	script := ""
	if focus != "" {
		script = "tell application " + appleString(focus) + " to activate\ndelay 0.3\n"
	}
	script += `tell application "System Events" to ` + press
	_, err := execOutput("osascript", "-e", script)
	return err
}
