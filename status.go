package main

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ボタンに表示する状態。On ならボタンを光らせ、Text があればボタンに表示する
type State struct {
	On    bool   `json:"on,omitempty"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// 組み込みの状態。"obs.muted:マイク" のように ":" の後ろは引数
var builtinStatuses = map[string]func(c *Config, arg string) (State, error){
	"audio.muted": func(*Config, string) (State, error) {
		a := getAudio()
		if a == nil {
			return State{}, fmt.Errorf("この PC では音量を取得できません")
		}
		return State{On: a.Muted}, nil
	},
	"audio.volume": func(*Config, string) (State, error) {
		a := getAudio()
		if a == nil {
			return State{}, fmt.Errorf("この PC では音量を取得できません")
		}
		return State{On: a.Muted, Text: fmt.Sprintf("%d%%", a.Volume)}, nil
	},
	"mic.muted": func(*Config, string) (State, error) {
		m, err := micMuted()
		return State{On: m}, err
	},
	"spotify.playing": func(*Config, string) (State, error) {
		t, err := spotifyTrack()
		return State{On: t.Playing}, err
	},
	"spotify.track": func(*Config, string) (State, error) {
		t, err := spotifyTrack()
		return State{On: t.Playing, Text: t.Title}, err
	},
	"obs.streaming":  obsOutputActive("GetStreamStatus"),
	"obs.recording":  obsOutputActive("GetRecordStatus"),
	"obs.virtualcam": obsOutputActive("GetVirtualCamStatus"),
	"obs.scene":      obsSceneStatus,
	"obs.muted":      obsMutedStatus,
}

// スマホが何台あっても同じ状態を何度も調べないよう、少しの間だけ覚えておく
type stateCache struct {
	mu sync.Mutex
	m  map[string]cachedState
}

type cachedState struct {
	st State
	at time.Time
}

var statusCache = &stateCache{m: map[string]cachedState{}}

func statusKey(b Button) string {
	if b.Status != "" {
		return "s:" + b.Status
	}
	return "c:" + b.StatusCmd
}

func (sc *stateCache) forget(b Button) {
	sc.mu.Lock()
	delete(sc.m, statusKey(b))
	sc.mu.Unlock()
}

func buttonStatus(c *Config, b Button) State {
	key := statusKey(b)
	statusCache.mu.Lock()
	if e, ok := statusCache.m[key]; ok && time.Since(e.at) < time.Second {
		statusCache.mu.Unlock()
		return e.st
	}
	statusCache.mu.Unlock()

	st := computeStatus(c, b)
	statusCache.mu.Lock()
	statusCache.m[key] = cachedState{st, time.Now()}
	statusCache.mu.Unlock()
	return st
}

func computeStatus(c *Config, b Button) State {
	var (
		st  State
		err error
	)
	if b.Status != "" {
		name, arg := splitArg(b.Status)
		f, ok := builtinStatuses[name]
		if !ok {
			return State{Error: "不明な status: " + b.Status}
		}
		st, err = f(c, arg)
	} else {
		st, err = shellStatus(b.StatusCmd)
	}
	if err != nil {
		return State{Error: err.Error()}
	}
	st.Text = shorten(st.Text, 40)
	return st
}

// コマンドの出力の 1 行目が on/true/1/yes なら On、off/false/0/no/空なら Off、それ以外はその文字を表示する
func shellStatus(cmdline string) (State, error) {
	cmd := shellCommand(cmdline)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		return State{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cmd.Process.Kill()
		return State{}, fmt.Errorf("statusCmd が 2 秒以内に終わりませんでした")
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
	line = strings.TrimSpace(line)
	switch strings.ToLower(line) {
	case "on", "true", "1", "yes":
		return State{On: true}, nil
	case "off", "false", "0", "no", "":
		return State{}, nil
	}
	return State{Text: line}, nil
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
