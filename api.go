package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const defaultPage = "メイン"

// ページの並び。pages に書かれた順のあと、ボタンにだけ書かれたページを出てきた順に足す
func (c *Config) pageNames() []string {
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, p := range c.Pages {
		add(p)
	}
	for _, b := range c.Buttons {
		add(b.Page)
	}
	if len(names) == 0 {
		names = []string{defaultPage}
	}
	return names
}

// ページを省略したボタンは最初のページに入れる
func (c *Config) pageOf(b Button, pages []string) string {
	if b.Page == "" {
		return pages[0]
	}
	return b.Page
}

func (s *server) handleButtons(w http.ResponseWriter, r *http.Request) {
	c, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	pages := c.pageNames()
	buttons := make([]publicButton, len(c.Buttons))
	for i, b := range c.Buttons {
		buttons[i] = publicButton{
			ID: b.ID, Label: b.Label, Icon: b.Icon, Image: b.Image, Color: b.Color, Page: c.pageOf(b, pages),
			W: b.W, H: b.H,
			OnIcon: b.OnIcon, OnColor: b.OnColor, HasStatus: b.Status != "" || b.StatusCmd != "",
			Hidden: b.Hidden,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"columns": c.Columns, "pages": pages, "buttons": buttons})
}

// スマホの設定画面から、ページごとの並び順・表示/非表示・列数だけを変える。
// 実行内容 (action / open / cmd / keys) は受け取らないので、スマホから任意コマンドは登録できない。
type layoutRequest struct {
	Columns int          `json:"columns"`
	Pages   []layoutPage `json:"pages"`
}

type layoutPage struct {
	Name   string   `json:"name"`
	Shown  []string `json:"shown"`  // 表示するボタンの ID（並び順）
	Hidden []string `json:"hidden"` // 隠すボタンの ID
}

func (s *server) handleLayout(w http.ResponseWriter, r *http.Request) {
	var req layoutRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Columns < 1 || req.Columns > 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "列の数は 1〜6 にしてください"})
		return
	}
	for _, p := range req.Pages {
		if strings.TrimSpace(p.Name) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ページ名が空です"})
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.Columns = req.Columns
	c.Pages, c.Buttons = applyLayout(c, req.Pages)
	if err := s.saveConfig(c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("%s layout updated", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// 送られてきたページの順にボタンを並べ直す。送られてこなかったボタン（設定画面を開いた後に
// buttons.json へ追加されたものなど）は元のページのまま後ろに残す
func applyLayout(c *Config, req []layoutPage) ([]string, []Button) {
	used := make([]bool, len(c.Buttons))
	take := func(id string) (Button, bool) {
		for i, b := range c.Buttons {
			if !used[i] && b.ID == id {
				used[i] = true
				return b, true
			}
		}
		return Button{}, false
	}
	pages := c.pageNames()
	var names []string
	var out []Button
	for _, p := range req {
		names = append(names, p.Name)
		for _, list := range []struct {
			ids    []string
			hidden bool
		}{{p.Shown, false}, {p.Hidden, true}} {
			for _, id := range list.ids {
				if b, ok := take(id); ok {
					b.Page, b.Hidden = p.Name, list.hidden
					out = append(out, b)
				}
			}
		}
	}
	for i, b := range c.Buttons {
		if !used[i] {
			b.Page = c.pageOf(b, pages)
			out = append(out, b)
		}
	}
	// ページの並びは送られてきた順。残ったボタンのページも消さない
	tmp := Config{Pages: names, Buttons: out}
	return tmp.pageNames(), out
}

// 書きかけのファイルが残らないよう、一時ファイルに書いてから置き換える
func (s *server) saveConfig(c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.configPath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.configPath)
}

// GET /api/status?ids=a,b,c  … 今のページのボタンの状態だけを調べる
func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	res := map[string]any{"audio": getAudio()}
	ids := strings.Split(r.URL.Query().Get("ids"), ",")
	if len(ids) == 0 || ids[0] == "" {
		writeJSON(w, http.StatusOK, res)
		return
	}
	c, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		states = map[string]State{}
	)
	for _, b := range c.Buttons {
		if !want[b.ID] || (b.Status == "" && b.StatusCmd == "") {
			continue
		}
		wg.Add(1)
		go func(b Button) {
			defer wg.Done()
			st := buttonStatus(c, b)
			mu.Lock()
			states[b.ID] = st
			mu.Unlock()
		}(b)
	}
	wg.Wait()
	res["states"] = states
	writeJSON(w, http.StatusOK, res)
}

func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, b := range c.Buttons {
		if b.ID != id {
			continue
		}
		msg, err := run(c, b)
		ok := err == nil
		if err != nil {
			msg = err.Error()
		}
		log.Printf("%s run %s -> ok=%v %s", r.RemoteAddr, id, ok, msg)
		res := map[string]any{"ok": ok, "message": msg, "audio": getAudio()}
		if b.Status != "" || b.StatusCmd != "" {
			// 押した結果がすぐ見えるよう、少し待ってから状態を返す（OBS などは反映に時間がかかる）
			time.Sleep(300 * time.Millisecond)
			statusCache.forget(b)
			res["state"] = buttonStatus(c, b)
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown button"})
}
