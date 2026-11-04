package main

import "runtime"

// 初回起動時に作る buttons.json。ページは「メイン」と、アプリごとのページ
func defaultConfig() Config {
	var buttons []Button
	for _, b := range defaultButtons() {
		b.Page = defaultPage
		buttons = append(buttons, b)
	}
	buttons = append(buttons, appButtons(runtime.GOOS)...)
	return Config{
		Columns: 3,
		Pages:   []string{defaultPage, "Zoom", "OBS", "YouTube", "Spotify", "Discord"},
		Buttons: buttons,
	}
}

// 「用意されたボタンから追加」だけに出すボタン（初回の buttons.json には入れない）
func extraButtons(goos string) []Button {
	pick := func(win, darwin, linux string) string {
		switch goos {
		case "windows":
			return win
		case "darwin":
			return darwin
		}
		return linux
	}
	const c = "#0891b2"
	g := "スクリーンショット・録画"
	bs := []Button{
		// 画面全体はすぐ保存される（Windows: ピクチャ\スクリーンショット、Mac: デスクトップ、GNOME: ピクチャ/スクリーンショット）
		{ID: "shot", Page: g, Label: "スクリーンショット", Icon: "📸", Color: c,
			Keys: pick("cmd+printscreen", "cmd+shift+3", "shift+printscreen")},
		// 範囲は PC 側で選ぶ
		{ID: "shot-area", Page: g, Label: "範囲を選んで撮る", Icon: "✂️", Color: c,
			Keys: pick("cmd+shift+s", "cmd+shift+4", "printscreen")},
	}
	if goos == "windows" {
		// Xbox Game Bar の録画。録画できるのはゲームやアプリのウィンドウで、デスクトップやエクスプローラーは録れない
		bs = append(bs, Button{ID: "gamebar-record", Page: g, Label: "録画 開始/停止", Icon: "⏺", Color: "#16a34a",
			Keys: "cmd+alt+r"})
	}
	return bs
}

// OS ごとに違うのは、アプリの起動方法と、ショートカットの修飾キー（Mac は ⌘）
func appButtons(goos string) []Button {
	mac := goos == "darwin"
	pick := func(win, darwin, linux string) string {
		switch goos {
		case "windows":
			return win
		case "darwin":
			return darwin
		}
		return linux
	}
	launch := func(b Button, win, darwin, linux string) Button {
		v := pick(win, darwin, linux)
		// "open:" で始まるものは URL などを開く、それ以外はコマンド
		if len(v) > 5 && v[:5] == "open:" {
			b.Open = v[5:]
		} else {
			b.Cmd = v
		}
		return b
	}
	keys := func(winLinux, darwin string) string {
		if mac {
			return darwin
		}
		return winLinux
	}
	// Mac は「アプリ名」で前面に出す。Windows / Linux はウィンドウタイトルの一部
	focus := func(winLinux, darwin string) string {
		if mac {
			return darwin
		}
		return winLinux
	}

	const (
		zoom    = "#2563eb"
		obsC    = "#475569"
		youtube = "#dc2626"
		spot    = "#16a34a"
		discord = "#5865f2"
		red     = "#ef4444"
	)
	var bs []Button
	add := func(page string, list ...Button) {
		for _, b := range list {
			b.Page = page
			bs = append(bs, b)
		}
	}

	// Zoom: ショートカットは Zoom の設定で「グローバルショートカットを有効にする」にすると、
	// Zoom が裏にあっても効く
	add("Zoom",
		launch(Button{ID: "zoom-open", Label: "Zoom を開く", Icon: "📹", Color: zoom},
			`start "" "%APPDATA%\Zoom\bin\Zoom.exe"`, "open -a zoom.us", "zoom || flatpak run us.zoom.Zoom"),
		Button{ID: "zoom-mic", Label: "マイク", Icon: "🎙", Color: zoom, Keys: keys("alt+a", "cmd+shift+a")},
		Button{ID: "zoom-video", Label: "ビデオ", Icon: "🎥", Color: zoom, Keys: keys("alt+v", "cmd+shift+v")},
		Button{ID: "zoom-share", Label: "画面共有", Icon: "🖥", Color: zoom, Keys: keys("alt+s", "cmd+shift+s")},
		Button{ID: "zoom-hand", Label: "手を挙げる", Icon: "✋", Color: zoom, Keys: keys("alt+y", "alt+y")},
		Button{ID: "zoom-leave", Label: "退出", Icon: "🚪", Color: red, Keys: keys("alt+q", "cmd+w")},
	)

	// OBS: obs-websocket で直接操作する（キー割り当ては不要）
	add("OBS",
		launch(Button{ID: "obs-open", Label: "OBS を開く", Icon: "🎬", Color: obsC},
			`start "" /D "%ProgramFiles%\obs-studio\bin\64bit" obs64.exe`, "open -a OBS", "obs || flatpak run com.obsproject.Studio"),
		Button{ID: "obs-stream", Label: "配信", Icon: "📡", Color: obsC, Action: "obs.stream.toggle",
			Status: "obs.streaming", OnIcon: "🔴", OnColor: red},
		Button{ID: "obs-record", Label: "録画", Icon: "⏺", Color: obsC, Action: "obs.record.toggle",
			Status: "obs.recording", OnIcon: "🔴", OnColor: red},
		Button{ID: "obs-prev", Label: "前のシーン", Icon: "⬅️", Color: obsC, Action: "obs.scene.prev"},
		Button{ID: "obs-scene", Label: "シーン", Icon: "🎞", Color: obsC, Action: "obs.scene.next", Status: "obs.scene"},
		Button{ID: "obs-next", Label: "次のシーン", Icon: "➡️", Color: obsC, Action: "obs.scene.next"},
		Button{ID: "obs-vcam", Label: "仮想カメラ", Icon: "📷", Color: obsC, Action: "obs.virtualcam.toggle",
			Status: "obs.virtualcam", OnColor: "#0ea5e9"},
	)

	// YouTube: ブラウザの YouTube のタブを前面に出してから、YouTube のショートカットを押す
	yt := func(id, label, icon, k string) Button {
		return Button{ID: id, Label: label, Icon: icon, Color: youtube, Keys: k, Focus: focus("YouTube", "")}
	}
	add("YouTube",
		Button{ID: "yt-open", Label: "YouTube を開く", Icon: "▶️", Color: youtube, Open: "https://www.youtube.com"},
		yt("yt-play", "再生/停止", "⏯", "k"),
		yt("yt-full", "全画面", "⛶", "f"),
		yt("yt-back", "10秒戻る", "⏪", "j"),
		yt("yt-mute", "ミュート", "🔇", "m"),
		yt("yt-fwd", "10秒進む", "⏩", "l"),
		yt("yt-prevv", "前の動画", "⏮", "shift+p"),
		yt("yt-cc", "字幕", "💬", "c"),
		yt("yt-nextv", "次の動画", "⏭", "shift+n"),
	)

	// Spotify: Spotify を名指しで操作するので、ほかのプレイヤーが動いていても大丈夫
	add("Spotify",
		Button{ID: "sp-prev", Label: "前の曲", Icon: "⏮", Color: spot, Action: "spotify.prev"},
		Button{ID: "sp-play", Label: "再生/停止", Icon: "▶️", Color: spot, Action: "spotify.playpause",
			Status: "spotify.playing", OnIcon: "⏸"},
		Button{ID: "sp-next", Label: "次の曲", Icon: "⏭", Color: spot, Action: "spotify.next"},
		Button{ID: "sp-voldown", Label: "音量 -", Icon: "🔉", Color: "#10b981", Action: "volume.down"},
		Button{ID: "sp-track", Label: "再生中の曲", Icon: "🎵", Color: spot, Action: "spotify.playpause",
			Status: "spotify.track"},
		Button{ID: "sp-volup", Label: "音量 +", Icon: "🔊", Color: "#10b981", Action: "volume.up"},
		launch(Button{ID: "sp-open", Label: "Spotify を開く", Icon: "🟢", Color: spot},
			"open:spotify:", "open -a Spotify", "spotify || flatpak run com.spotify.Client"),
	)

	// Discord: Discord を前面に出してから、Discord のショートカットを押す
	add("Discord",
		launch(Button{ID: "dc-open", Label: "Discord を開く", Icon: "💬", Color: discord},
			"open:discord:", "open -a Discord", "discord || flatpak run com.discordapp.Discord"),
		Button{ID: "dc-mute", Label: "マイクミュート", Icon: "🎙", Color: discord,
			Keys: keys("ctrl+shift+m", "cmd+shift+m"), Focus: focus("Discord", "Discord")},
		Button{ID: "dc-deafen", Label: "スピーカーミュート", Icon: "🎧", Color: discord,
			Keys: keys("ctrl+shift+d", "cmd+shift+d"), Focus: focus("Discord", "Discord")},
	)
	return bs
}
