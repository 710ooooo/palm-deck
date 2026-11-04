package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Audio struct {
	Volume int  `json:"volume"`
	Muted  bool `json:"muted"`
}

// 組み込み操作。中身は OS ごとのファイル (platform_*.go) や obs.go / spotify にある。
// "obs.scene:シーン名" のように、":" の後ろは引数として渡す
var builtinActions = map[string]func(c *Config, arg string) error{
	"media.playpause": noArg(func() error { return media(mediaPlayPause) }),
	"media.next":      noArg(func() error { return media(mediaNext) }),
	"media.prev":      noArg(func() error { return media(mediaPrev) }),
	"volume.up":       noArg(func() error { return volume(+5) }),
	"volume.down":     noArg(func() error { return volume(-5) }),
	"volume.mute":     noArg(toggleMute),
	"mic.mute":        noArg(toggleMic),
	"screen.lock":     noArg(lockScreen),
	"screen.off":      noArg(screenOff),

	"spotify.playpause": noArg(func() error { return spotify(mediaPlayPause) }),
	"spotify.next":      noArg(func() error { return spotify(mediaNext) }),
	"spotify.prev":      noArg(func() error { return spotify(mediaPrev) }),

	"obs.stream.toggle":     obsSimple("ToggleStream"),
	"obs.record.toggle":     obsSimple("ToggleRecord"),
	"obs.virtualcam.toggle": obsSimple("ToggleVirtualCam"),
	"obs.scene":             obsSetScene,
	"obs.scene.next":        func(c *Config, _ string) error { return obsStepScene(c, +1) },
	"obs.scene.prev":        func(c *Config, _ string) error { return obsStepScene(c, -1) },
	"obs.mute":              obsToggleMute,
}

func noArg(f func() error) func(*Config, string) error {
	return func(*Config, string) error { return f() }
}

type mediaKey int

const (
	mediaPlayPause mediaKey = iota
	mediaNext
	mediaPrev
)

func splitArg(s string) (name, arg string) {
	name, arg, _ = strings.Cut(s, ":")
	return name, arg
}

func run(c *Config, b Button) (string, error) {
	switch {
	case b.Action != "":
		name, arg := splitArg(b.Action)
		f, ok := builtinActions[name]
		if !ok {
			return "", fmt.Errorf("不明な action: %s", b.Action)
		}
		return "", f(c, arg)
	case b.Keys != "":
		k, err := parseKeys(b.Keys)
		if err != nil {
			return "", err
		}
		return "", sendKeys(k, b.Focus)
	case b.Text != "":
		return "", typeText(b.Text, b.Enter, b.Focus)
	case b.Open != "":
		return "started", openTarget(expandHome(b.Open))
	case b.Cmd != "":
		return runShell(b.Cmd)
	}
	return "", errors.New("action / keys / text / open / cmd のどれかを指定してください")
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// コマンドを実行して 3 秒だけ結果を待つ。アプリ起動など長く動くものはそのまま走らせておく
func runShell(cmdline string) (string, error) {
	cmd := shellCommand(cmdline)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		msg := strings.TrimSpace(out.String())
		if err != nil && msg != "" {
			return "", errors.New(msg)
		}
		return msg, err
	case <-time.After(3 * time.Second):
		return "started", nil
	}
}

// 結果を待たずに終わるタイプのコマンド用（出力をエラーメッセージに使う）
func execOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil && s != "" {
		return s, errors.New(s)
	}
	return s, err
}
