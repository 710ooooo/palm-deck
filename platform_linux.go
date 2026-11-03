package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

func defaultButtons() []Button {
	return append(commonButtons(),
		Button{ID: "browser", Label: "ブラウザ", Icon: "🌐", Color: "#8b5cf6", Open: "https://www.google.com"},
		Button{ID: "files", Label: "ファイル", Icon: "📁", Color: "#8b5cf6", Open: "~"},
		Button{ID: "terminal", Label: "ターミナル", Icon: "💻", Color: "#8b5cf6", Cmd: "x-terminal-emulator || ptyxis --new-window || gnome-terminal || konsole"},
		Button{ID: "lock", Label: "画面ロック", Icon: "🔒", Color: "#64748b", Action: "screen.lock"},
		Button{ID: "off", Label: "画面オフ", Icon: "🌙", Color: "#64748b", Action: "screen.off"},
		Button{ID: "mic", Label: "マイクミュート", Icon: "🎙", Color: "#f59e0b", Action: "mic.mute", Status: "mic.muted", OnIcon: "🔇", OnColor: "#ef4444"},
	)
}

func shellCommand(s string) *exec.Cmd { return exec.Command("sh", "-c", s) }

// 起動したアプリがサーバー終了で巻き込まれないように別セッションにする
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func openTarget(t string) error {
	cmd := exec.Command("xdg-open", t)
	detach(cmd)
	return cmd.Start()
}

func has(name string) bool { _, err := exec.LookPath(name); return err == nil }

// --- メディア: playerctl があれば使い、なければ D-Bus (MPRIS) を直接叩く ---

func media(k mediaKey) error {
	method := [...]string{"PlayPause", "Next", "Previous"}[k]
	if has("playerctl") {
		_, err := execOutput("playerctl", [...]string{"play-pause", "next", "previous"}[k])
		return err
	}
	player, err := mprisPlayer()
	if err != nil {
		return err
	}
	return mprisCall(player, method)
}

var mprisName = regexp.MustCompile(`org\.mpris\.MediaPlayer2\.[^"]+`)

func mprisCall(player, method string) error {
	_, err := execOutput("dbus-send", "--session", "--type=method_call", "--dest="+player,
		"/org/mpris/MediaPlayer2", "org.mpris.MediaPlayer2.Player."+method)
	return err
}

func mprisGet(player, prop string) (string, error) {
	return execOutput("dbus-send", "--session", "--print-reply", "--dest="+player,
		"/org/mpris/MediaPlayer2", "org.freedesktop.DBus.Properties.Get",
		"string:org.mpris.MediaPlayer2.Player", "string:"+prop)
}

// 再生中のプレイヤーを優先、なければ最初に見つかったもの
func mprisPlayer() (string, error) {
	out, err := execOutput("dbus-send", "--session", "--print-reply", "--dest=org.freedesktop.DBus",
		"/org/freedesktop/DBus", "org.freedesktop.DBus.ListNames")
	if err != nil {
		return "", err
	}
	players := mprisName.FindAllString(out, -1)
	if len(players) == 0 {
		return "", errors.New("再生中のプレイヤーがありません")
	}
	for _, p := range players {
		status, _ := mprisGet(p, "PlaybackStatus")
		if strings.Contains(status, `"Playing"`) {
			return p, nil
		}
	}
	return players[0], nil
}

// --- 音量: PipeWire (wpctl) を優先、なければ PulseAudio (pactl) ---

func volume(delta int) error {
	step := strconv.Itoa(abs(delta)) + "%"
	if has("wpctl") {
		if delta > 0 {
			_, err := execOutput("wpctl", "set-volume", "-l", "1.0", "@DEFAULT_AUDIO_SINK@", step+"+")
			return err
		}
		_, err := execOutput("wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", step+"-")
		return err
	}
	sign := "+"
	if delta < 0 {
		sign = "-"
	}
	_, err := execOutput("pactl", "set-sink-volume", "@DEFAULT_SINK@", sign+step)
	return err
}

func toggleMute() error {
	if has("wpctl") {
		_, err := execOutput("wpctl", "set-mute", "@DEFAULT_AUDIO_SINK@", "toggle")
		return err
	}
	_, err := execOutput("pactl", "set-sink-mute", "@DEFAULT_SINK@", "toggle")
	return err
}

var (
	wpctlVol = regexp.MustCompile(`Volume:\s*([\d.]+)`)
	pactlVol = regexp.MustCompile(`(\d+)%`)
)

func getAudio() *Audio {
	if has("wpctl") {
		out, err := execOutput("wpctl", "get-volume", "@DEFAULT_AUDIO_SINK@")
		m := wpctlVol.FindStringSubmatch(out)
		if err != nil || m == nil {
			return nil
		}
		v, _ := strconv.ParseFloat(m[1], 64)
		return &Audio{Volume: int(v*100 + 0.5), Muted: strings.Contains(out, "MUTED")}
	}
	if has("pactl") {
		out, err := execOutput("pactl", "get-sink-volume", "@DEFAULT_SINK@")
		m := pactlVol.FindStringSubmatch(out)
		if err != nil || m == nil {
			return nil
		}
		v, _ := strconv.Atoi(m[1])
		mute, _ := execOutput("pactl", "get-sink-mute", "@DEFAULT_SINK@")
		return &Audio{Volume: v, Muted: strings.Contains(mute, "yes")}
	}
	return nil
}

func lockScreen() error {
	_, err := execOutput("loginctl", "lock-session")
	return err
}

func screenOff() error {
	// GNOME / KDE の順に試す
	if _, err := execOutput("busctl", "--user", "call", "org.gnome.ScreenSaver", "/org/gnome/ScreenSaver",
		"org.gnome.ScreenSaver", "SetActive", "b", "true"); err == nil {
		return nil
	}
	_, err := execOutput("busctl", "--user", "call", "org.freedesktop.ScreenSaver", "/ScreenSaver",
		"org.freedesktop.ScreenSaver", "SetActive", "b", "true")
	return err
}

// --- マイク ---

// 既定のマイクが決まっていないと、wpctl の @DEFAULT_AUDIO_SOURCE@ はスピーカーを指してしまう。
// そのときは wpctl status の Sources 一覧から本物のマイクを選ぶ
var wpctlSourceLine = regexp.MustCompile(`^[\s│├└─]*(\*?)\s*(\d+)\.`)

func wpctlMic() (string, error) {
	if out, _ := execOutput("wpctl", "inspect", "@DEFAULT_AUDIO_SOURCE@"); strings.Contains(out, `media.class = "Audio/Source"`) {
		return "@DEFAULT_AUDIO_SOURCE@", nil
	}
	out, err := execOutput("wpctl", "status")
	if err != nil {
		return "", err
	}
	inSources, first := false, ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Sources:") {
			inSources = true
			continue
		}
		if !inSources {
			continue
		}
		m := wpctlSourceLine.FindStringSubmatch(line)
		if m == nil {
			if strings.HasSuffix(strings.TrimSpace(line), ":") {
				break // 次の一覧 (Filters: など) に入った
			}
			continue
		}
		if m[1] == "*" {
			return m[2], nil
		}
		if first == "" {
			first = m[2]
		}
	}
	if first == "" {
		return "", errors.New("マイクが見つかりません")
	}
	return first, nil
}

func toggleMic() error {
	if has("wpctl") {
		mic, err := wpctlMic()
		if err != nil {
			return err
		}
		_, err = execOutput("wpctl", "set-mute", mic, "toggle")
		return err
	}
	_, err := execOutput("pactl", "set-source-mute", "@DEFAULT_SOURCE@", "toggle")
	return err
}

func micMuted() (bool, error) {
	if has("wpctl") {
		mic, err := wpctlMic()
		if err != nil {
			return false, err
		}
		out, err := execOutput("wpctl", "get-volume", mic)
		return strings.Contains(out, "MUTED"), err
	}
	out, err := execOutput("pactl", "get-source-mute", "@DEFAULT_SOURCE@")
	return strings.Contains(out, "yes"), err
}

// --- Spotify: MPRIS で Spotify を名指しで操作する ---

const spotifyBus = "org.mpris.MediaPlayer2.spotify"

func spotify(k mediaKey) error {
	if err := mprisCall(spotifyBus, [...]string{"PlayPause", "Next", "Previous"}[k]); err != nil {
		return errors.New("Spotify が起動していません")
	}
	return nil
}

type track struct {
	Title   string
	Playing bool
}

var (
	mprisTitle  = regexp.MustCompile(`"xesam:title"\s*variant\s*string "((?:[^"\\]|\\.)*)"`)
	mprisArtist = regexp.MustCompile(`"xesam:artist"\s*variant\s*array \[\s*string "((?:[^"\\]|\\.)*)"`)
)

// 起動していないときはエラーにせず「何も再生していない」とする
func spotifyTrack() (track, error) {
	status, err := mprisGet(spotifyBus, "PlaybackStatus")
	if err != nil {
		return track{}, nil
	}
	t := track{Playing: strings.Contains(status, `"Playing"`)}
	meta, _ := mprisGet(spotifyBus, "Metadata")
	if m := mprisTitle.FindStringSubmatch(meta); m != nil {
		t.Title = m[1]
		if a := mprisArtist.FindStringSubmatch(meta); a != nil && a[1] != "" {
			t.Title = a[1] + " - " + t.Title
		}
	}
	return t, nil
}

// --- キー操作 ---
// X11 で動くアプリは xdotool、Wayland では ydotool（全アプリに効くが、ウィンドウを前面に出せない）

func sendKeys(k keyCombo, focus string) error {
	useX := has("xdotool") && os.Getenv("DISPLAY") != ""
	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	if focus != "" {
		if !useX {
			return errors.New("ウィンドウを前面に出すには xdotool が必要です（sudo apt install xdotool）")
		}
		ids, _ := execOutput("xdotool", "search", "--onlyvisible", "--name", focus)
		id, _, _ := strings.Cut(ids, "\n")
		if id == "" {
			return fmt.Errorf("「%s」のウィンドウが見つかりません", focus)
		}
		if _, err := execOutput("xdotool", "windowactivate", "--sync", id); err != nil {
			return err
		}
		return xdotoolKey(k)
	}
	switch {
	case wayland && has("ydotool"):
		return ydotoolKey(k)
	case useX:
		return xdotoolKey(k)
	case has("ydotool"):
		return ydotoolKey(k)
	}
	return errors.New("キー操作には xdotool か ydotool が必要です（sudo apt install xdotool ydotool）")
}

var xdotoolNames = map[string]string{
	"space": "space", "enter": "Return", "esc": "Escape", "tab": "Tab", "backspace": "BackSpace",
	"delete": "Delete", "up": "Up", "down": "Down", "left": "Left", "right": "Right",
	"home": "Home", "end": "End", "pageup": "Prior", "pagedown": "Next",
	"minus": "minus", "equal": "equal", "comma": "comma", "period": "period", "slash": "slash",
}

func xdotoolKey(k keyCombo) error {
	var parts []string
	for _, m := range []struct {
		on   bool
		name string
	}{{k.Ctrl, "ctrl"}, {k.Shift, "shift"}, {k.Alt, "alt"}, {k.Cmd, "super"}} {
		if m.on {
			parts = append(parts, m.name)
		}
	}
	key := k.Key
	if n, ok := xdotoolNames[key]; ok {
		key = n
	} else if strings.HasPrefix(key, "f") && len(key) > 1 {
		key = strings.ToUpper(key)
	}
	_, err := execOutput("xdotool", "key", "--clearmodifiers", strings.Join(append(parts, key), "+"))
	return err
}

// Linux の入力イベントのキーコード (linux/input-event-codes.h)
var linuxKeyCodes = func() map[string]int {
	m := map[string]int{
		"esc": 1, "minus": 12, "equal": 13, "backspace": 14, "tab": 15, "enter": 28,
		"comma": 51, "period": 52, "slash": 53, "space": 57, "f11": 87, "f12": 88,
		"home": 102, "up": 103, "pageup": 104, "left": 105, "right": 106, "end": 107,
		"down": 108, "pagedown": 109, "delete": 111,
	}
	for i, row := range []string{"1234567890", "qwertyuiop", "asdfghjkl", "zxcvbnm"} {
		start := [...]int{2, 16, 30, 44}[i]
		for j, c := range row {
			m[string(c)] = start + j
		}
	}
	for i := 1; i <= 10; i++ {
		m[fmt.Sprintf("f%d", i)] = 58 + i
	}
	return m
}()

func ydotoolKey(k keyCombo) error {
	var mods []int
	for _, m := range []struct {
		on   bool
		code int
	}{{k.Ctrl, 29}, {k.Shift, 42}, {k.Alt, 56}, {k.Cmd, 125}} {
		if m.on {
			mods = append(mods, m.code)
		}
	}
	code := linuxKeyCodes[k.Key]
	args := []string{"key"}
	for _, m := range mods {
		args = append(args, fmt.Sprintf("%d:1", m))
	}
	args = append(args, fmt.Sprintf("%d:1", code), fmt.Sprintf("%d:0", code))
	for i := len(mods) - 1; i >= 0; i-- {
		args = append(args, fmt.Sprintf("%d:0", mods[i]))
	}
	if _, err := execOutput("ydotool", args...); err != nil {
		return fmt.Errorf("ydotool が動きません（ydotoold が起動しているか確認してください）: %w", err)
	}
	return nil
}
