package main

// ボタンの画像アイコン。設定フォルダの icons/ に「中身のハッシュ.拡張子」の名前で保存する。
// アプリのロゴは配布物に入れず、使う人が編集画面から公式サイトのアイコンを取ってくる。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxIconSize = 2 << 20

var iconName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// 画像の種類を中身から判定して拡張子を返す（拡張子や Content-Type は信用しない）
func iconExt(b []byte) (string, error) {
	switch http.DetectContentType(b) {
	case "image/png":
		return "png", nil
	case "image/jpeg":
		return "jpg", nil
	case "image/gif":
		return "gif", nil
	case "image/webp":
		return "webp", nil
	case "image/x-icon", "image/vnd.microsoft.icon":
		return "ico", nil
	}
	head := strings.ToLower(string(b[:min(len(b), 1024)]))
	if strings.Contains(head, "<svg") {
		return "svg", nil
	}
	return "", errors.New("PNG・JPEG・GIF・WebP・ICO・SVG の画像を選んでください")
}

func (s *server) saveIcon(b []byte) (string, error) {
	if len(b) == 0 {
		return "", errors.New("画像が空です")
	}
	if len(b) > maxIconSize {
		return "", errors.New("画像が大きすぎます（2MB まで）")
	}
	ext, err := iconExt(b)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	name := hex.EncodeToString(h[:8]) + "." + ext
	if err := os.MkdirAll(s.iconDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(s.iconDir, name)
	if _, err := os.Stat(path); err == nil {
		return name, nil // 同じ画像はもう入っている
	}
	return name, os.WriteFile(path, b, 0o644)
}

// GET /icons/{name}。スマホの <img> から読むのでトークンは要らない（名前は中身のハッシュで推測しにくい）
func (s *server) handleIcon(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !iconName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(filepath.Join(s.iconDir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	types := map[string]string{"png": "image/png", "jpg": "image/jpeg", "gif": "image/gif",
		"webp": "image/webp", "ico": "image/x-icon", "svg": "image/svg+xml"}
	w.Header().Set("Content-Type", types[strings.TrimPrefix(filepath.Ext(name), ".")])
	// SVG を直接開かれても中のスクリプトが動かないようにする
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Write(b)
}

// POST /api/edit/icon … 編集画面で選んだ画像ファイルをそのまま送る
func (s *server) handleIconUpload(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxIconSize+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "読み込めません"})
		return
	}
	name, err := s.saveIcon(b)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// POST /api/edit/icon/fetch {"url": "https://www.youtube.com"} … サイトのアイコンを取ってくる
func (s *server) handleIconFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	b, err := fetchSiteIcon(r.Context(), req.URL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	name, err := s.saveIcon(b)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

var iconClient = &http.Client{Timeout: 15 * time.Second}

func httpGet(ctx context.Context, u string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	// ブラウザ以外を断るサイトがあるので、ブラウザのふりをする
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	res, err := iconClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%s: %s", u, res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit))
	return b, res.Request.URL.String(), err
}

var (
	linkTag  = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	attrExpr = regexp.MustCompile(`(?is)([a-z-]+)\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	sizeExpr = regexp.MustCompile(`(\d+)x\d+`)
)

type iconCandidate struct {
	url   string
	score int // 大きいほど先に試す
}

// ページの <link rel="icon"> などから、大きいアイコンの順に候補を出す
func iconCandidates(page []byte, base *url.URL) []iconCandidate {
	var cs []iconCandidate
	for _, tag := range linkTag.FindAll(page, -1) {
		attrs := map[string]string{}
		for _, m := range attrExpr.FindAllSubmatch(tag, -1) {
			attrs[strings.ToLower(string(m[1]))] = strings.Trim(string(m[2]), `"'`)
		}
		rel := strings.ToLower(attrs["rel"])
		if !strings.Contains(rel, "icon") || strings.Contains(rel, "mask-icon") || attrs["href"] == "" {
			continue
		}
		u, err := base.Parse(attrs["href"])
		if err != nil {
			continue
		}
		score := 32
		switch {
		case strings.Contains(attrs["type"], "svg") || strings.HasSuffix(u.Path, ".svg"):
			score = 1000
		case sizeExpr.MatchString(attrs["sizes"]):
			score, _ = strconv.Atoi(sizeExpr.FindStringSubmatch(attrs["sizes"])[1])
		case strings.Contains(rel, "apple-touch-icon"):
			score = 180
		case strings.HasSuffix(u.Path, ".ico"):
			score = 48
		}
		cs = append(cs, iconCandidate{u.String(), score})
	}
	// ページに書かれていなくても置いてあることが多い場所
	for _, p := range []struct {
		path  string
		score int
	}{{"/apple-touch-icon.png", 150}, {"/favicon.ico", 16}} {
		u, _ := base.Parse(p.path)
		cs = append(cs, iconCandidate{u.String(), p.score})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].score > cs[j].score })
	return cs
}

func fetchSiteIcon(ctx context.Context, raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("https:// で始まる URL を入れてください")
	}
	page, final, err := httpGet(ctx, u.String(), 4<<20)
	if err != nil {
		return nil, fmt.Errorf("サイトを開けません: %v", err)
	}
	// URL が画像そのものだった
	if _, err := iconExt(page); err == nil && len(page) <= maxIconSize {
		return page, nil
	}
	base, _ := url.Parse(final)
	tried := map[string]bool{}
	for _, c := range iconCandidates(page, base) {
		if tried[c.url] {
			continue
		}
		tried[c.url] = true
		b, _, err := httpGet(ctx, c.url, maxIconSize+1)
		if err != nil || len(b) > maxIconSize {
			continue
		}
		if _, err := iconExt(b); err == nil && !bytes.Equal(b, page) {
			return b, nil
		}
	}
	return nil, errors.New("このサイトのアイコンが見つかりませんでした。画像ファイルを選んでください")
}
