package agent

import "strings"

// TruncRunes shortens s to at most n runes, appending an ellipsis when it cuts.
//
// Slicing a Go string by bytes splits multi-byte characters in half. Every
// label in this project's UI is Korean, where almost every character is three
// bytes, so byte-slicing produced invalid UTF-8 on most inputs.
func TruncRunes(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
