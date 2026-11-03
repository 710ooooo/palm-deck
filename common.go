package main

import (
	"fmt"

	"rsc.io/qr"
)

// どの OS でも同じ、メディアと音量のボタン（上 2 段）
func commonButtons() []Button {
	return []Button{
		{ID: "prev", Label: "前の曲", Icon: "⏮", Color: "#3b82f6", Action: "media.prev"},
		{ID: "play", Label: "再生/停止", Icon: "⏯", Color: "#3b82f6", Action: "media.playpause"},
		{ID: "next", Label: "次の曲", Icon: "⏭", Color: "#3b82f6", Action: "media.next"},
		{ID: "voldown", Label: "音量 -", Icon: "🔉", Color: "#10b981", Action: "volume.down"},
		{ID: "mute", Label: "ミュート", Icon: "🔈", Color: "#64748b", Action: "volume.mute", Status: "audio.muted", OnIcon: "🔇", OnColor: "#ef4444"},
		{ID: "volup", Label: "音量 +", Icon: "🔊", Color: "#10b981", Action: "volume.up"},
	}
}

// ターミナルに QR コードを描く（上下 2 マスを 1 文字にまとめる）
func printQR(text string) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return
	}
	const quiet = 2
	n := code.Size
	black := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < n && y < n && code.Black(x, y)
	}
	for y := -quiet; y < n+quiet; y += 2 {
		line := ""
		for x := -quiet; x < n+quiet; x++ {
			top, bottom := black(x, y), black(x, y+1)
			switch {
			case top && bottom:
				line += " "
			case top:
				line += "▄"
			case bottom:
				line += "▀"
			default:
				line += "█"
			}
		}
		fmt.Println(line)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
