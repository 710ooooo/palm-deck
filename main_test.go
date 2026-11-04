package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// obs-websocket v5 の代わりをする小さなサーバー（パスワード認証つき）
func fakeOBS(t *testing.T, password string) (url string, scene *string) {
	cur := "シーン1"
	scenes := []string{"シーン3", "シーン2", "シーン1"} // OBS は画面の下から順に返す
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"obswebsocket.json"}})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := context.Background()
		send := func(op int, d any) {
			b, _ := json.Marshal(map[string]any{"op": op, "d": d})
			conn.Write(ctx, websocket.MessageText, b)
		}
		read := func() (int, map[string]any) {
			_, b, err := conn.Read(ctx)
			if err != nil {
				return -1, nil
			}
			var m struct {
				Op int            `json:"op"`
				D  map[string]any `json:"d"`
			}
			json.Unmarshal(b, &m)
			return m.Op, m.D
		}
		send(0, map[string]any{"rpcVersion": 1, "authentication": map[string]string{"challenge": "ch", "salt": "sa"}})
		if op, d := read(); op != 1 || d["authentication"] != obsAuth(password, "sa", "ch") {
			conn.Close(4009, "auth failed")
			return
		}
		send(2, map[string]any{"negotiatedRpcVersion": 1})
		for {
			op, d := read()
			if op != 6 {
				return
			}
			res := map[string]any{"requestType": d["requestType"], "requestId": d["requestId"],
				"requestStatus": map[string]any{"result": true, "code": 100}}
			data, _ := d["requestData"].(map[string]any)
			switch d["requestType"] {
			case "GetSceneList":
				var list []map[string]any
				for i, s := range scenes {
					list = append(list, map[string]any{"sceneName": s, "sceneIndex": i})
				}
				res["responseData"] = map[string]any{"currentProgramSceneName": cur, "scenes": list}
			case "GetCurrentProgramScene":
				res["responseData"] = map[string]any{"currentProgramSceneName": cur}
			case "SetCurrentProgramScene":
				cur = data["sceneName"].(string)
			case "GetRecordStatus":
				res["responseData"] = map[string]any{"outputActive": true, "outputTimecode": "00:01:02.345"}
			default:
				res["requestStatus"] = map[string]any{"result": false, "code": 204, "comment": "unknown request"}
			}
			send(7, res)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), &cur
}

func TestOBS(t *testing.T) {
	url, cur := fakeOBS(t, "secret")
	obs = &obsClient{}
	c := &Config{OBS: &OBSConfig{URL: url, Password: "secret"}}

	if err := obsStepScene(c, +1); err != nil || *cur != "シーン2" {
		t.Fatalf("次のシーン: err=%v cur=%s", err, *cur)
	}
	if err := obsStepScene(c, -1); err != nil || *cur != "シーン1" {
		t.Fatalf("前のシーン: err=%v cur=%s", err, *cur)
	}
	if err := obsStepScene(c, -1); err != nil || *cur != "シーン3" {
		t.Fatalf("前のシーン（先頭から最後へ）: err=%v cur=%s", err, *cur)
	}
	if _, err := run(c, Button{Action: "obs.scene:シーン2"}); err != nil || *cur != "シーン2" {
		t.Fatalf("シーン指定: err=%v cur=%s", err, *cur)
	}
	st := computeStatus(c, Button{Status: "obs.scene"})
	if st.Text != "シーン2" {
		t.Fatalf("シーン名の表示: %+v", st)
	}
	if st := computeStatus(c, Button{Status: "obs.scene:シーン2"}); !st.On {
		t.Fatalf("シーン指定の状態: %+v", st)
	}
	if st := computeStatus(c, Button{Status: "obs.recording"}); !st.On || st.Text != "00:01:02" {
		t.Fatalf("録画中: %+v", st)
	}
	// OBS が返すエラーはそのまま伝え、接続は切らない
	if err := obs.request(c, "Nope", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown request") {
		t.Fatalf("OBS のエラー: %v", err)
	}
	if obs.conn == nil {
		t.Fatal("OBS のエラーで接続が切れた")
	}

	// パスワード違い
	obs = &obsClient{}
	bad := &Config{OBS: &OBSConfig{URL: url, Password: "wrong"}}
	if err := obs.request(bad, "GetCurrentProgramScene", nil, nil); err == nil || !strings.Contains(err.Error(), "パスワードが違います") {
		t.Fatalf("パスワード違い: %v", err)
	}
	// OBS が起動していない
	obs = &obsClient{}
	off := &Config{OBS: &OBSConfig{URL: "ws://127.0.0.1:1"}}
	if err := obs.request(off, "GetCurrentProgramScene", nil, nil); err != errOBSConnect {
		t.Fatalf("未起動: %v", err)
	}
}

func TestParseKeys(t *testing.T) {
	cases := map[string]keyCombo{
		"ctrl+shift+m": {Ctrl: true, Shift: true, Key: "m"},
		"Alt+A":        {Alt: true, Key: "a"},
		"cmd+shift+a":  {Cmd: true, Shift: true, Key: "a"},
		"option+y":     {Alt: true, Key: "y"},
		"shift+n":      {Shift: true, Key: "n"},
		"k":            {Key: "k"},
		"f5":           {Key: "f5"},
		"ctrl+Return":  {Ctrl: true, Key: "enter"},
	}
	for in, want := range cases {
		got, err := parseKeys(in)
		if err != nil || got != want {
			t.Errorf("parseKeys(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ctrl+", "ctrl+shift", "ctrl+hoge", "m+ctrl"} {
		if _, err := parseKeys(bad); err == nil {
			t.Errorf("parseKeys(%q) がエラーになりません", bad)
		}
	}
}

func TestApplyLayout(t *testing.T) {
	c := &Config{
		Pages: []string{"メイン", "OBS"},
		Buttons: []Button{
			{ID: "a"}, {ID: "b", Page: "メイン"}, {ID: "o1", Page: "OBS"}, {ID: "o2", Page: "OBS"},
			{ID: "new", Page: "新しい"}, // 設定画面を開いた後に追加されたボタン
		},
	}
	pages, buttons := applyLayout(c, []layoutPage{
		{Name: "OBS", Shown: []string{"o2", "a"}, Hidden: []string{"o1"}},
		{Name: "メイン", Shown: []string{"b"}},
	})
	if want := []string{"OBS", "メイン", "新しい"}; !reflect.DeepEqual(pages, want) {
		t.Errorf("pages = %v; want %v", pages, want)
	}
	var got []string
	for _, b := range buttons {
		s := b.Page + "/" + b.ID
		if b.Hidden {
			s += "(隠)"
		}
		got = append(got, s)
	}
	if want := []string{"OBS/o2", "OBS/a", "OBS/o1(隠)", "メイン/b", "新しい/new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("buttons = %v; want %v", got, want)
	}
}

func TestShellStatus(t *testing.T) {
	for out, want := range map[string]State{
		"on": {On: true}, "TRUE": {On: true}, "off": {}, "": {}, "曲名 - 歌手\n2行目": {Text: "曲名 - 歌手"},
	} {
		got, err := shellStatus("printf '" + out + "'")
		if err != nil || got != want {
			t.Errorf("shellStatus(%q) = %+v, %v; want %+v", out, got, err, want)
		}
	}
}

func TestDefaultConfig(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		seen := map[string]bool{}
		for _, b := range append(appButtons(goos), extraButtons(goos)...) {
			if seen[b.ID] {
				t.Errorf("%s: id %q が重複しています", goos, b.ID)
			}
			seen[b.ID] = true
			n := 0
			for _, v := range []string{b.Action, b.Open, b.Cmd, b.Keys, b.Text} {
				if v != "" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s: %s の動作が %d 個あります", goos, b.ID, n)
			}
			if b.Keys != "" {
				if _, err := parseKeys(b.Keys); err != nil {
					t.Errorf("%s: %s: %v", goos, b.ID, err)
				}
			}
			if name, _ := splitArg(b.Action); b.Action != "" && builtinActions[name] == nil {
				t.Errorf("%s: %s: 不明な action %s", goos, b.ID, b.Action)
			}
			if name, _ := splitArg(b.Status); b.Status != "" && builtinStatuses[name] == nil {
				t.Errorf("%s: %s: 不明な status %s", goos, b.ID, b.Status)
			}
		}
	}
	c := defaultConfig()
	if got := c.pageNames(); !reflect.DeepEqual(got, []string{"メイン", "Zoom", "OBS", "YouTube", "Spotify", "Discord"}) {
		t.Errorf("pages = %v", got)
	}
}

func TestCatalogCoversBuiltins(t *testing.T) {
	names := map[string]bool{}
	for _, a := range actionCatalog {
		names[a.Name] = true
		if builtinActions[a.Name] == nil {
			t.Errorf("編集画面の操作 %s がありません", a.Name)
		}
	}
	for n := range builtinActions {
		if !names[n] {
			t.Errorf("操作 %s が編集画面の一覧にありません", n)
		}
	}
	names = map[string]bool{}
	for _, s := range statusCatalog {
		names[s.Name] = true
		if builtinStatuses[s.Name] == nil {
			t.Errorf("編集画面の状態 %s がありません", s.Name)
		}
	}
	for n := range builtinStatuses {
		if !names[n] {
			t.Errorf("状態 %s が編集画面の一覧にありません", n)
		}
	}
}

func TestValidateConfig(t *testing.T) {
	ok := func() *Config {
		return &Config{Columns: 3, Pages: []string{"メイン", "配信"}, Buttons: []Button{
			{ID: "a", Page: "メイン", Action: "obs.scene:シーン1", Status: "obs.scene:シーン1"},
			{ID: "b", Page: "配信", Keys: "ctrl+shift+m"},
			{ID: "c", Page: "配信", StatusCmd: "date"}, // 表示だけのボタンも OK
			{ID: "d", Page: "配信", Text: "テストを実行して", Enter: true},
		}}
	}
	if err := validateConfig(ok()); err != nil {
		t.Fatalf("正しい設定がエラー: %v", err)
	}
	if err := validateConfig(defaultConfigPtr()); err != nil {
		t.Fatalf("初期設定がエラー: %v", err)
	}
	bad := map[string]func(c *Config){
		"列":      func(c *Config) { c.Columns = 7 },
		"ページなし":  func(c *Config) { c.Pages = nil },
		"ページ重複":  func(c *Config) { c.Pages = []string{"メイン", "メイン", "配信"} },
		"ID 重複":  func(c *Config) { c.Buttons[1].ID = "a" },
		"ID 空":   func(c *Config) { c.Buttons[1].ID = "" },
		"ID 記号":  func(c *Config) { c.Buttons[1].ID = "a/b" },
		"ないページ":  func(c *Config) { c.Buttons[1].Page = "なし" },
		"動作 2 つ": func(c *Config) { c.Buttons[1].Cmd = "ls" },
		"入力とキー":  func(c *Config) { c.Buttons[3].Keys = "enter" },
		"不明な操作":  func(c *Config) { c.Buttons[0].Action = "nope" },
		"キー":     func(c *Config) { c.Buttons[1].Keys = "ctrl+" },
		"状態 2 つ": func(c *Config) { c.Buttons[0].StatusCmd = "date" },
		"不明な状態":  func(c *Config) { c.Buttons[0].Status = "nope" },
	}
	for name, f := range bad {
		c := ok()
		f(c)
		if err := validateConfig(c); err == nil {
			t.Errorf("%s: エラーになりません", name)
		}
	}
}

func defaultConfigPtr() *Config { c := defaultConfig(); return &c }

func TestEditAccess(t *testing.T) {
	dir := t.TempDir()
	s := &server{configPath: dir + "/buttons.json", token: "phone", editToken: "edit", port: "8765"}
	if err := ensureConfig(s.configPath); err != nil {
		t.Fatal(err)
	}
	h := s.editAuth(s.handleEditGet)
	do := func(remote, host, tok string) int {
		r := httptest.NewRequest("GET", "/api/edit/config", nil)
		r.RemoteAddr, r.Host = remote, host
		if tok != "" {
			r.Header.Set("X-Edit-Token", tok)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}
	for _, c := range []struct {
		name, remote, host, tok string
		want                    int
	}{
		{"PC から", "127.0.0.1:5000", "localhost:8765", "edit", 200},
		{"IPv6", "[::1]:5000", "[::1]:8765", "edit", 200},
		{"スマホから", "192.168.1.20:5000", "192.168.1.10:8765", "edit", 403},
		{"トークンなし", "127.0.0.1:5000", "localhost:8765", "", 403},
		{"トークン違い", "127.0.0.1:5000", "localhost:8765", "phone", 403},
		{"DNS rebinding", "127.0.0.1:5000", "evil.example:8765", "edit", 403},
	} {
		if got := do(c.remote, c.host, c.tok); got != c.want {
			t.Errorf("%s: %d; want %d", c.name, got, c.want)
		}
	}

	// 保存: 古い version だと断る
	get := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/edit/config", nil)
	r.RemoteAddr, r.Host = "127.0.0.1:5000", "localhost:8765"
	r.Header.Set("X-Edit-Token", "edit")
	h(get, r)
	var st editState
	json.Unmarshal(get.Body.Bytes(), &st)
	st.Config.Pages = append(st.Config.Pages, "新しいページ")
	put := func(st editState) int {
		b, _ := json.Marshal(st)
		r := httptest.NewRequest("PUT", "/api/edit/config", strings.NewReader(string(b)))
		r.RemoteAddr, r.Host = "127.0.0.1:5000", "localhost:8765"
		r.Header.Set("X-Edit-Token", "edit")
		w := httptest.NewRecorder()
		s.editAuth(s.handleEditPut)(w, r)
		return w.Code
	}
	if code := put(st); code != 200 {
		t.Fatalf("保存: %d", code)
	}
	if code := put(st); code != http.StatusConflict {
		t.Fatalf("古い version で保存できてしまう: %d", code)
	}
	c, _ := s.loadConfig()
	if got := c.pageNames(); got[len(got)-1] != "新しいページ" {
		t.Fatalf("空のページが保存されていない: %v", got)
	}
}

func TestIconCandidates(t *testing.T) {
	page := []byte(`<head>
<link rel="shortcut icon" href="https://cdn.example/favicon.ico" type="image/x-icon">
<link rel="icon" href="/img/fav32.png" sizes="32x32">
<link href="/img/fav96.png" sizes="96x96" rel="icon">
<link rel='apple-touch-icon' href='touch.png'>
<link rel="mask-icon" href="/mask.svg">
<link rel="stylesheet" href="/a.css">
</head>`)
	base, _ := url.Parse("https://www.example.com/watch/")
	var got []string
	for _, c := range iconCandidates(page, base) {
		got = append(got, fmt.Sprintf("%s=%d", c.url, c.score))
	}
	want := []string{
		"https://www.example.com/watch/touch.png=180",
		"https://www.example.com/apple-touch-icon.png=150",
		"https://www.example.com/img/fav96.png=96",
		"https://cdn.example/favicon.ico=48",
		"https://www.example.com/img/fav32.png=32",
		"https://www.example.com/favicon.ico=16",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("候補:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestIcons(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	ico := []byte("\x00\x00\x01\x00\x01\x00\x10\x10")
	s := &server{iconDir: t.TempDir()}
	for in, ext := range map[string]string{string(png): "png", string(svg): "svg", string(ico): "ico"} {
		name, err := s.saveIcon([]byte(in))
		if err != nil || !strings.HasSuffix(name, "."+ext) || !iconName.MatchString(name) {
			t.Errorf("saveIcon(%s) = %q, %v", ext, name, err)
		}
	}
	if _, err := s.saveIcon([]byte("hello")); err == nil {
		t.Error("画像でないものが保存できてしまう")
	}
	if _, err := s.saveIcon(append(png, make([]byte, maxIconSize)...)); err == nil {
		t.Error("大きすぎる画像が保存できてしまう")
	}

	name, _ := s.saveIcon(svg)
	get := func(n string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/icons/"+n, nil)
		r.SetPathValue("name", n)
		w := httptest.NewRecorder()
		s.handleIcon(w, r)
		return w
	}
	w := get(name)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("SVG の配信: %d %v", w.Code, w.Header())
	}
	for _, bad := range []string{"../token", "..%2Ftoken", "abc.png", "0123456789abcdef.exe"} {
		if w := get(bad); w.Code != 404 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}
