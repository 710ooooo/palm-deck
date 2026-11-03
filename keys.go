package main

import (
	"fmt"
	"strings"
)

// "ctrl+shift+m" のようなキー操作。修飾キーは ctrl / shift / alt / cmd の 4 つに揃える
// （option は alt、win / super / meta は cmd と同じ。Windows では Windows キー、Linux では Super キー）
type keyCombo struct {
	Ctrl, Shift, Alt, Cmd bool
	Key                   string // a〜z, 0〜9, f1〜f12, space, enter, esc, tab, up など
}

var keyAliases = map[string]string{
	"control": "ctrl", "option": "alt", "opt": "alt",
	"command": "cmd", "win": "cmd", "super": "cmd", "meta": "cmd",
	"return": "enter", "escape": "esc", "del": "delete", "bs": "backspace",
	"pgup": "pageup", "pgdn": "pagedown", "arrowup": "up", "arrowdown": "down",
	"arrowleft": "left", "arrowright": "right", ".": "period", ",": "comma",
	"-": "minus", "=": "equal", "/": "slash",
}

// どの OS でも使えるキー名（OS ごとのキーコード表はこの名前で引く）
var keyNames = func() map[string]bool {
	m := map[string]bool{}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = true
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = true
	}
	for i := 1; i <= 12; i++ {
		m[fmt.Sprintf("f%d", i)] = true
	}
	for _, k := range strings.Fields("space enter esc tab backspace delete up down left right home end pageup pagedown minus equal comma period slash") {
		m[k] = true
	}
	return m
}()

func parseKeys(s string) (keyCombo, error) {
	var k keyCombo
	parts := strings.Split(strings.ToLower(strings.ReplaceAll(s, " ", "")), "+")
	for i, p := range parts {
		if a, ok := keyAliases[p]; ok {
			p = a
		}
		last := i == len(parts)-1
		switch {
		case p == "ctrl" && !last:
			k.Ctrl = true
		case p == "shift" && !last:
			k.Shift = true
		case p == "alt" && !last:
			k.Alt = true
		case p == "cmd" && !last:
			k.Cmd = true
		case last && keyNames[p]:
			k.Key = p
		default:
			return k, fmt.Errorf("keys %q の %q が分かりません", s, p)
		}
	}
	return k, nil
}
