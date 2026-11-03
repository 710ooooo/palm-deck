package main

// PC のブラウザで使う編集画面 (/edit)。ボタンの中身（コマンドやキー操作）まで変えられるので、
// この PC から localhost で開いたときだけ使える。スマホからは今までどおり並べ替えしかできない。

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
)

//go:embed editor/edit.html
var editHTML string

func newEditToken() string {
	buf := make([]byte, 16)
	rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// この PC のブラウザから、localhost の名前で開いているか。
// Host も見るのは、悪いサイトが自分のドメインを 127.0.0.1 に向けて (DNS rebinding) 中身を読むのを防ぐため
func isLocalRequest(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return false
	}
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func (s *server) handleEdit(w http.ResponseWriter, r *http.Request) {
	if !isLocalRequest(r) {
		http.Error(w, "編集画面はこの PC のブラウザで http://localhost:"+s.port+"/edit を開いてください", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	fmt.Fprint(w, strings.Replace(editHTML, "__EDIT_TOKEN__", s.editToken, 1))
}

// 編集用 API。X-Edit-Token は編集画面の HTML にだけ埋め込んであるので、
// ほかのサイトからは送れない（独自ヘッダーなので、別のサイトからの fetch はブラウザが止める）
func (s *server) editAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLocalRequest(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Edit-Token")), []byte(s.editToken)) != 1 {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		h(w, r)
	}
}

// 編集中にスマホで並べ替えたときなどに、古い内容で上書きしないための目印
func (s *server) configVersion() (string, error) {
	b, err := os.ReadFile(s.configPath)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8]), nil
}

type editState struct {
	Version string  `json:"version"`
	Config  *Config `json:"config"`
}

func (s *server) handleEditGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	v, _ := s.configVersion()
	c.Pages = c.pageNames()
	pages := c.Pages
	for i := range c.Buttons {
		c.Buttons[i].Page = c.pageOf(c.Buttons[i], pages)
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v, "config": c, "catalog": editCatalog()})
}

func (s *server) handleEditPut(w http.ResponseWriter, r *http.Request) {
	var req editState
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Config == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := validateConfig(req.Config); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, _ := s.configVersion(); v != req.Version {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "編集中に buttons.json が変わりました（スマホで並べ替えた、ファイルを直接書き換えた など）。「読み込み直す」を押してから、もう一度変更してください"})
		return
	}
	if err := s.saveConfig(req.Config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	v, _ := s.configVersion()
	log.Printf("%s config saved from editor", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": v})
}

// 保存する前に、編集中のボタンを実行して試す（編集画面の「実行」ボタン）
func (s *server) handleEditRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Button Button     `json:"button"`
		OBS    *OBSConfig `json:"obs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	c, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.OBS = req.OBS
	msg, err := run(c, req.Button)
	res := map[string]any{"ok": err == nil, "message": msg}
	if err != nil {
		res["message"] = err.Error()
	}
	if req.Button.Status != "" || req.Button.StatusCmd != "" {
		res["state"] = computeStatus(c, req.Button)
	}
	writeJSON(w, http.StatusOK, res)
}

func validateConfig(c *Config) error {
	if c.Columns < 1 || c.Columns > 6 {
		return fmt.Errorf("列の数は 1〜6 にしてください")
	}
	if len(c.Pages) == 0 {
		return fmt.Errorf("ページが 1 つもありません")
	}
	pages := map[string]bool{}
	for _, p := range c.Pages {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("名前が空のページがあります")
		}
		if pages[p] {
			return fmt.Errorf("ページ「%s」が 2 つあります", p)
		}
		pages[p] = true
	}
	ids := map[string]bool{}
	for _, b := range c.Buttons {
		name := b.Label
		if name == "" {
			name = b.ID
		}
		if strings.TrimSpace(b.ID) == "" {
			return fmt.Errorf("ボタン「%s」の ID が空です", name)
		}
		if strings.ContainsAny(b.ID, "/?#,") {
			return fmt.Errorf("ボタン「%s」の ID に / ? # , は使えません", name)
		}
		if ids[b.ID] {
			return fmt.Errorf("ID「%s」のボタンが 2 つあります", b.ID)
		}
		ids[b.ID] = true
		if !pages[b.Page] {
			return fmt.Errorf("ボタン「%s」のページ「%s」がありません", name, b.Page)
		}
		n := 0
		for _, v := range []string{b.Action, b.Keys, b.Open, b.Cmd} {
			if v != "" {
				n++
			}
		}
		if n > 1 {
			return fmt.Errorf("ボタン「%s」の動作が %d 個あります。1 つにしてください", name, n)
		}
		if b.Action != "" {
			if a, _ := splitArg(b.Action); builtinActions[a] == nil {
				return fmt.Errorf("ボタン「%s」: 不明な操作 %s", name, b.Action)
			}
		}
		if b.Keys != "" {
			if _, err := parseKeys(b.Keys); err != nil {
				return fmt.Errorf("ボタン「%s」: %v", name, err)
			}
		}
		if b.W < 0 || b.W > 6 || b.H < 0 || b.H > 4 {
			return fmt.Errorf("ボタン「%s」の大きさは横 1〜6 マス、縦 1〜4 マスにしてください", name)
		}
		if b.Image != "" && !iconName.MatchString(b.Image) {
			return fmt.Errorf("ボタン「%s」の画像の名前が正しくありません", name)
		}
		if b.Status != "" && b.StatusCmd != "" {
			return fmt.Errorf("ボタン「%s」: status と statusCmd はどちらか 1 つにしてください", name)
		}
		if b.Status != "" {
			if st, _ := splitArg(b.Status); builtinStatuses[st] == nil {
				return fmt.Errorf("ボタン「%s」: 不明な状態 %s", name, b.Status)
			}
		}
	}
	return nil
}

// ---- 編集画面に出す選択肢 ----

type catalogItem struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Arg   string `json:"arg,omitempty"` // 引数が要るときの説明
}

var actionCatalog = []catalogItem{
	{"media.playpause", "再生/停止（今鳴っているプレイヤー）", ""},
	{"media.next", "次の曲", ""},
	{"media.prev", "前の曲", ""},
	{"volume.up", "音量を上げる", ""},
	{"volume.down", "音量を下げる", ""},
	{"volume.mute", "スピーカーのミュート", ""},
	{"mic.mute", "マイクのミュート（Windows は未対応）", ""},
	{"screen.lock", "画面ロック", ""},
	{"screen.off", "画面オフ", ""},
	{"spotify.playpause", "Spotify: 再生/停止", ""},
	{"spotify.next", "Spotify: 次の曲", ""},
	{"spotify.prev", "Spotify: 前の曲", ""},
	{"obs.stream.toggle", "OBS: 配信の開始/停止", ""},
	{"obs.record.toggle", "OBS: 録画の開始/停止", ""},
	{"obs.virtualcam.toggle", "OBS: 仮想カメラの開始/停止", ""},
	{"obs.scene.next", "OBS: 次のシーン", ""},
	{"obs.scene.prev", "OBS: 前のシーン", ""},
	{"obs.scene", "OBS: シーンを切り替える", "シーン名"},
	{"obs.mute", "OBS: 音声ソースのミュート", "音声ソース名（例: マイク/AUX）"},
}

var statusCatalog = []catalogItem{
	{"audio.muted", "スピーカーがミュート中なら光る", ""},
	{"audio.volume", "スピーカーの音量を表示", ""},
	{"mic.muted", "マイクがミュート中なら光る（Windows は未対応）", ""},
	{"spotify.playing", "Spotify が再生中なら光る", ""},
	{"spotify.track", "Spotify の曲名を表示", ""},
	{"obs.streaming", "OBS: 配信中なら光る（経過時間も表示）", ""},
	{"obs.recording", "OBS: 録画中なら光る（経過時間も表示）", ""},
	{"obs.virtualcam", "OBS: 仮想カメラ使用中なら光る", ""},
	{"obs.scene", "OBS: シーン名を表示 / 指定したシーンなら光る", "シーン名（空なら今のシーン名を表示）"},
	{"obs.muted", "OBS: 音声ソースがミュート中なら光る", "音声ソース名"},
}

type editTemplate struct {
	Group  string `json:"group"`
	Button Button `json:"button"`
}

func editCatalog() map[string]any {
	var templates []editTemplate
	for _, b := range defaultConfig().Buttons {
		g := b.Page
		b.Page, b.Hidden = "", false
		templates = append(templates, editTemplate{Group: g, Button: b})
	}
	return map[string]any{"actions": actionCatalog, "statuses": statusCatalog, "templates": templates}
}
