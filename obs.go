package main

// OBS Studio 28 以降に入っている obs-websocket (v5) で OBS を操作する。
// OBS の「ツール → WebSocket サーバー設定」で「WebSocket サーバーを有効にする」をオンにして使う。

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type OBSConfig struct {
	URL      string `json:"url,omitempty"`      // 省略時は ws://127.0.0.1:4455
	Password string `json:"password,omitempty"` // WebSocket サーバー設定の「認証を有効にする」のパスワード
}

var errOBSConnect = errors.New("OBS に接続できません。OBS を起動し、「ツール → WebSocket サーバー設定」で WebSocket サーバーを有効にしてください")

type obsClient struct {
	mu   sync.Mutex
	conn *websocket.Conn
	key  string // 接続先とパスワード。変わったらつなぎ直す
	seq  int
}

var obs = &obsClient{}

type obsMessage struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

func (o *obsClient) request(c *Config, typ string, data any, out any) error {
	cfg := OBSConfig{}
	if c.OBS != nil {
		cfg = *c.OBS
	}
	if cfg.URL == "" {
		cfg.URL = "ws://127.0.0.1:4455"
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if key := cfg.URL + "\x00" + cfg.Password; o.conn == nil || o.key != key {
		o.close()
		if err := o.connect(ctx, cfg); err != nil {
			return err
		}
		o.key = key
	}
	err := o.call(ctx, typ, data, out)
	var reqErr *obsRequestError
	if err != nil && !errors.As(err, &reqErr) {
		// 通信が切れた（OBS を閉じたなど）ので、次は最初からつなぎ直す
		o.close()
		return errOBSConnect
	}
	return err
}

func (o *obsClient) close() {
	if o.conn != nil {
		o.conn.CloseNow()
		o.conn = nil
	}
}

func (o *obsClient) connect(ctx context.Context, cfg OBSConfig) error {
	conn, _, err := websocket.Dial(ctx, cfg.URL, &websocket.DialOptions{Subprotocols: []string{"obswebsocket.json"}})
	if err != nil {
		return errOBSConnect
	}
	conn.SetReadLimit(4 << 20)

	// Hello (op 0) → Identify (op 1) → Identified (op 2)
	var hello struct {
		Authentication *struct {
			Challenge string `json:"challenge"`
			Salt      string `json:"salt"`
		} `json:"authentication"`
	}
	if err := readOp(ctx, conn, 0, &hello); err != nil {
		conn.CloseNow()
		return errOBSConnect
	}
	identify := map[string]any{"rpcVersion": 1, "eventSubscriptions": 0}
	if a := hello.Authentication; a != nil {
		if cfg.Password == "" {
			conn.CloseNow()
			return errors.New(`OBS にパスワードが設定されています。buttons.json の "obs": {"password": "..."} に書いてください`)
		}
		identify["authentication"] = obsAuth(cfg.Password, a.Salt, a.Challenge)
	}
	if err := writeOp(ctx, conn, 1, identify); err != nil {
		conn.CloseNow()
		return errOBSConnect
	}
	if err := readOp(ctx, conn, 2, nil); err != nil {
		conn.CloseNow()
		if websocket.CloseStatus(err) == 4009 {
			return errors.New("OBS のパスワードが違います。buttons.json の obs.password を確認してください")
		}
		return errOBSConnect
	}
	o.conn = conn
	return nil
}

// base64(sha256(base64(sha256(password + salt)) + challenge))
func obsAuth(password, salt, challenge string) string {
	h := sha256.Sum256([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(h[:])
	h = sha256.Sum256([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(h[:])
}

func writeOp(ctx context.Context, conn *websocket.Conn, op int, d any) error {
	b, err := json.Marshal(map[string]any{"op": op, "d": d})
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

// 指定した op のメッセージが来るまで読む（それ以外は読み捨てる）
func readOp(ctx context.Context, conn *websocket.Conn, op int, out any) error {
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var m obsMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		if m.Op != op {
			continue
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(m.D, out)
	}
}

type obsRequestError struct{ msg string }

func (e *obsRequestError) Error() string { return e.msg }

func (o *obsClient) call(ctx context.Context, typ string, data any, out any) error {
	o.seq++
	id := strconv.Itoa(o.seq)
	req := map[string]any{"requestType": typ, "requestId": id}
	if data != nil {
		req["requestData"] = data
	}
	if err := writeOp(ctx, o.conn, 6, req); err != nil {
		return err
	}
	for {
		var res struct {
			RequestID     string `json:"requestId"`
			RequestStatus struct {
				Result  bool   `json:"result"`
				Code    int    `json:"code"`
				Comment string `json:"comment"`
			} `json:"requestStatus"`
			ResponseData json.RawMessage `json:"responseData"`
		}
		if err := readOp(ctx, o.conn, 7, &res); err != nil {
			return err
		}
		if res.RequestID != id {
			continue
		}
		if !res.RequestStatus.Result {
			msg := res.RequestStatus.Comment
			if msg == "" {
				msg = fmt.Sprintf("code %d", res.RequestStatus.Code)
			}
			return &obsRequestError{"OBS: " + msg}
		}
		if out != nil && len(res.ResponseData) > 0 {
			return json.Unmarshal(res.ResponseData, out)
		}
		return nil
	}
}

// ---- 操作 ----

func obsSimple(typ string) func(*Config, string) error {
	return func(c *Config, _ string) error { return obs.request(c, typ, nil, nil) }
}

func obsSetScene(c *Config, name string) error {
	if name == "" {
		return errors.New(`シーン名を "obs.scene:シーン名" の形で書いてください`)
	}
	return obs.request(c, "SetCurrentProgramScene", map[string]string{"sceneName": name}, nil)
}

// OBS の画面に並んでいる順で、次（前）のシーンに切り替える
func obsStepScene(c *Config, step int) error {
	scenes, cur, err := obsScenes(c)
	if err != nil {
		return err
	}
	if len(scenes) == 0 {
		return errors.New("OBS にシーンがありません")
	}
	i := 0
	for j, s := range scenes {
		if s == cur {
			i = j
		}
	}
	i = (i + step + len(scenes)) % len(scenes)
	return obsSetScene(c, scenes[i])
}

func obsScenes(c *Config) (names []string, current string, err error) {
	var res struct {
		Current string `json:"currentProgramSceneName"`
		Scenes  []struct {
			Name  string `json:"sceneName"`
			Index int    `json:"sceneIndex"`
		} `json:"scenes"`
	}
	if err := obs.request(c, "GetSceneList", nil, &res); err != nil {
		return nil, "", err
	}
	// scenes は画面の下から順に入っているので、上から順に並べ直す
	for i := len(res.Scenes) - 1; i >= 0; i-- {
		names = append(names, res.Scenes[i].Name)
	}
	return names, res.Current, nil
}

func obsToggleMute(c *Config, input string) error {
	if input == "" {
		return errors.New(`音声ソース名を "obs.mute:マイク/AUX" の形で書いてください`)
	}
	return obs.request(c, "ToggleInputMute", map[string]string{"inputName": input}, nil)
}

// ---- 状態 ----

func obsOutputActive(typ string) func(*Config, string) (State, error) {
	return func(c *Config, _ string) (State, error) {
		var res struct {
			Active   bool   `json:"outputActive"`
			Timecode string `json:"outputTimecode"`
		}
		if err := obs.request(c, typ, nil, &res); err != nil {
			return State{}, err
		}
		st := State{On: res.Active}
		if res.Active && res.Timecode != "" {
			st.Text, _, _ = strings.Cut(res.Timecode, ".") // 00:12:34.567 → 00:12:34
		}
		return st, nil
	}
}

// 引数なし: 今のシーン名を表示する。"obs.scene:シーン名": そのシーンが表示中なら On
func obsSceneStatus(c *Config, name string) (State, error) {
	var res struct {
		Current string `json:"currentProgramSceneName"`
	}
	if err := obs.request(c, "GetCurrentProgramScene", nil, &res); err != nil {
		return State{}, err
	}
	if name == "" {
		return State{Text: res.Current}, nil
	}
	return State{On: res.Current == name}, nil
}

func obsMutedStatus(c *Config, input string) (State, error) {
	var res struct {
		Muted bool `json:"inputMuted"`
	}
	if err := obs.request(c, "GetInputMute", map[string]string{"inputName": input}, &res); err != nil {
		return State{}, err
	}
	return State{On: res.Muted}, nil
}
