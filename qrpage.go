package main

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"rsc.io/qr"
)

var qrTemplate = template.Must(template.New("qr").Parse(`<!doctype html>
<html lang="ja">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Palm Deck</title>
<style>
  :root { --bg: #f8fafc; --fg: #0f172a; --muted: #64748b; --card: #fff; }
  @media (prefers-color-scheme: dark) { :root { --bg: #0f172a; --fg: #e2e8f0; --muted: #94a3b8; --card: #1e293b; } }
  body { margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
    background: var(--bg); color: var(--fg); font-family: system-ui, -apple-system, "Hiragino Sans", "Yu Gothic UI", sans-serif; }
  main { text-align: center; padding: 24px 16px; }
  h1 { font-size: 22px; margin: 0 0 4px; }
  p { color: var(--muted); margin: 0 0 20px; line-height: 1.7; }
  .qr { background: #fff; padding: 20px; border-radius: 20px; display: inline-block;
    box-shadow: 0 8px 30px #0002; }
  .qr svg { width: min(320px, 80vw); height: auto; display: block; }
  .url { margin-top: 16px; font-size: 13px; color: var(--muted); word-break: break-all; }
  .other { margin-top: 24px; font-size: 13px; color: var(--muted); }
</style>
</head>
<body>
<main>
  <h1>スマホで読み取ってください</h1>
  <p>PC と同じ Wi-Fi につないだスマホのカメラで読み取ると、ボタン画面が開きます。<br>開いたらブラウザのメニューから「ホーム画面に追加」すると便利です。</p>
  {{range $i, $q := .}}{{if $i}}<div class="other">つながらないときはこちら</div>{{end}}
  <div class="qr">{{$q.SVG}}</div>
  <div class="url">{{$q.URL}}</div>
  {{end}}
  <div class="other"><a href="/edit" style="color:inherit">ボタンを追加・編集する</a></div>
</main>
</body>
</html>`))

type qrEntry struct {
	URL string
	SVG template.HTML
}

// トークン入りの URL を見せるので、この PC 自身からのアクセスだけに限る
func (s *server) handleQR(w http.ResponseWriter, r *http.Request) {
	if !isLocalRequest(r) {
		http.Error(w, "この PC のブラウザで http://localhost:"+s.port+"/qr を開いてください", http.StatusForbidden)
		return
	}
	var entries []qrEntry
	for _, u := range s.urls {
		svg, err := qrSVG(u)
		if err != nil {
			continue
		}
		entries = append(entries, qrEntry{URL: u, SVG: svg})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	qrTemplate.Execute(w, entries)
}

func qrSVG(text string) (template.HTML, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	n := code.Size
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges"><path fill="#000" d="`, n, n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x, y)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return template.HTML(b.String()), nil
}
