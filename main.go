// Palm Deck: スマホのブラウザを Stream Deck 代わりにする小さなサーバー。
//
// buttons.json に書いたボタンだけを実行する。スマホからは任意コマンドを送れない。
// LAN 内の他人に押されないよう、トークン付き URL でアクセスする。
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

//go:embed static
var staticFiles embed.FS

var version = "dev"

type Button struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Icon      string `json:"icon,omitempty"`      // 絵文字
	Image     string `json:"image,omitempty"`     // 画像アイコン（設定フォルダの icons/ の中のファイル名）。icon より優先
	ImageFill bool   `json:"imageFill,omitempty"` // 画像をボタン全体に広げる（端は切れる）
	Color     string `json:"color,omitempty"`
	Page      string `json:"page,omitempty"` // 表示するページ（省略すると最初のページ）
	W         int    `json:"w,omitempty"`    // 横に何マス使うか（省略すると 1）
	H         int    `json:"h,omitempty"`    // 縦に何マス使うか（省略すると 1）

	// 押したときの動作。どれか 1 つを書く
	Action string `json:"action,omitempty"` // 組み込み操作 (media.playpause, obs.scene:シーン名 など)
	Open   string `json:"open,omitempty"`   // URL やファイル・フォルダを開く
	Cmd    string `json:"cmd,omitempty"`    // シェルコマンド
	Keys   string `json:"keys,omitempty"`   // キー操作 (ctrl+shift+m など)
	Focus  string `json:"focus,omitempty"`  // keys / text の前に前面に出すウィンドウ（タイトルの一部。Mac はアプリ名）
	Text   string `json:"text,omitempty"`   // 文字を入力する
	Enter  bool   `json:"enter,omitempty"`  // text を入力したあと Enter を押す

	// 状態の表示。どちらか 1 つを書く
	Status    string `json:"status,omitempty"`    // 組み込みの状態 (obs.streaming, spotify.track など)
	StatusCmd string `json:"statusCmd,omitempty"` // 状態を調べるシェルコマンド（on/off か、表示する文字を出力する）
	OnIcon    string `json:"onIcon,omitempty"`    // 状態が on のときのアイコン
	OnColor   string `json:"onColor,omitempty"`   // 状態が on のときの色

	Hidden bool `json:"hidden,omitempty"` // スマホの画面に出さない（設定画面で切り替え）
}

type Config struct {
	Columns int        `json:"columns"`
	Pages   []string   `json:"pages,omitempty"` // ページの並び順
	OBS     *OBSConfig `json:"obs,omitempty"`
	Buttons []Button   `json:"buttons"`
}

// スマホに返すボタン情報（実行内容は渡さない）
type publicButton struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Icon      string `json:"icon,omitempty"`
	Image     string `json:"image,omitempty"`
	ImageFill bool   `json:"imageFill,omitempty"`
	Color     string `json:"color,omitempty"`
	Page      string `json:"page"`
	W         int    `json:"w,omitempty"`
	H         int    `json:"h,omitempty"`
	OnIcon    string `json:"onIcon,omitempty"`
	OnColor   string `json:"onColor,omitempty"`
	HasStatus bool   `json:"hasStatus,omitempty"`
	Hidden    bool   `json:"hidden,omitempty"`
}

type server struct {
	mu         sync.Mutex // buttons.json の読み書きを直列にする
	configPath string
	iconDir    string // 画像アイコンの置き場所
	token      string
	editToken  string // 編集画面 (/edit) 用。起動するたびに変わる
	port       string
	urls       []string // スマホで開く URL（QR ページ用）
}

func main() {
	port := flag.Int("port", 8765, "待ち受けポート")
	dir := flag.String("dir", defaultDir(), "設定ファイル (buttons.json) とトークンの置き場所")
	showVersion := flag.Bool("version", false, "バージョンを表示")
	noBrowser := flag.Bool("no-browser", false, "起動時に QR コードのページを PC のブラウザで開かない")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	s := &server{configPath: filepath.Join(*dir, "buttons.json")}
	if err := ensureConfig(s.configPath); err != nil {
		log.Fatal(err)
	}
	token, err := loadToken(filepath.Join(*dir, "token"))
	if err != nil {
		log.Fatal(err)
	}
	s.token = token
	s.editToken = newEditToken()
	s.iconDir = filepath.Join(*dir, "icons")
	s.port = fmt.Sprint(*port)

	static, _ := fs.Sub(staticFiles, "static")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("GET /qr", s.handleQR)
	mux.HandleFunc("GET /api/buttons", s.auth(s.handleButtons))
	mux.HandleFunc("GET /api/status", s.auth(s.handleStatus))
	mux.HandleFunc("POST /api/run/{id}", s.auth(s.handleRun))
	mux.HandleFunc("POST /api/layout", s.auth(s.handleLayout))
	mux.HandleFunc("GET /edit", s.handleEdit)
	mux.HandleFunc("GET /api/edit/config", s.editAuth(s.handleEditGet))
	mux.HandleFunc("PUT /api/edit/config", s.editAuth(s.handleEditPut))
	mux.HandleFunc("POST /api/edit/run", s.editAuth(s.handleEditRun))
	mux.HandleFunc("POST /api/edit/icon", s.editAuth(s.handleIconUpload))
	mux.HandleFunc("POST /api/edit/icon/fetch", s.editAuth(s.handleIconFetch))
	mux.HandleFunc("GET /icons/{name}", s.handleIcon)

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("ポート %d を開けません（すでに起動していませんか？）: %v", *port, err)
	}

	fmt.Printf("Palm Deck %s\n", version)
	fmt.Printf("設定ファイル: %s\n\n", s.configPath)
	ips := lanIPs()
	if len(ips) == 0 {
		ips = []string{"<PCのIP>"}
	}
	for _, ip := range ips {
		s.urls = append(s.urls, fmt.Sprintf("http://%s:%d/#t=%s", ip, *port, token))
	}
	fmt.Println("スマホで QR コードを読み取るか、URL を開いてください:")
	printQR(s.urls[0])
	for _, u := range s.urls {
		fmt.Println("  " + u)
	}
	qrPage := fmt.Sprintf("http://localhost:%d/qr", *port)
	fmt.Printf("\nQR コードはこの PC のブラウザでも表示できます: %s\n", qrPage)
	fmt.Printf("ボタンの追加や編集はこの PC のブラウザで: http://localhost:%d/edit\n", *port)
	fmt.Println("終了するには Ctrl+C")

	if !*noBrowser {
		if err := openTarget(qrPage); err != nil {
			log.Printf("ブラウザを開けませんでした: %v", err)
		}
	}
	log.Fatal(http.Serve(ln, mux))
}

func defaultDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "palm-deck")
	}
	return "."
}

func ensureConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	b, err := json.MarshalIndent(defaultConfig(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func loadToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := base64.RawURLEncoding.EncodeToString(buf)
	return t, os.WriteFile(path, []byte(t), 0o600)
}

// 毎回読み直すので、buttons.json を編集したらスマホ側を再読み込みするだけで反映される
func (s *server) loadConfig() (*Config, error) {
	b, err := os.ReadFile(s.configPath)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("buttons.json の書式が正しくありません: %w", err)
	}
	if c.Columns <= 0 {
		c.Columns = 3
	}
	return &c, nil
}

func (s *server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Token")), []byte(s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

var virtualIface = regexp.MustCompile(`(?i)^(docker|br-|veth|virbr|vmnet|vboxnet|utun|lo)`)

func lanIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || virtualIface.MatchString(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() {
					ips = append(ips, ip4.String())
				}
			}
		}
	}
	return ips
}
