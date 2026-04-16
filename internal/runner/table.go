package runner

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"switch-monitor/internal/adapter"
)

const (
	headerTx = "Tx (packets OK/Fail or MB)"
	headerRx = "Rx (packets OK/Fail or MB)"
)

// statusRow holds a fully-resolved row for display.
type statusRow struct {
	switchName string
	portID     int
	linkUp     bool
	speedMbps  *int
	alias      string

	txOk   *int64
	txFail *int64
	rxOk   *int64
	rxFail *int64

	txMBytes *float64
	rxMBytes *float64
}

func portDisplay(r statusRow) string {
	if r.alias != "" {
		return fmt.Sprintf("%d · %s", r.portID, r.alias)
	}
	return fmt.Sprintf("%d", r.portID)
}

func txCell(r statusRow) string {
	if r.txOk != nil || r.txFail != nil {
		return packetsCell(r.txOk, r.txFail)
	}
	if r.txMBytes != nil {
		return fmt.Sprintf("%.2f MB", *r.txMBytes)
	}
	return "-"
}

func rxCell(r statusRow) string {
	if r.rxOk != nil || r.rxFail != nil {
		return packetsCell(r.rxOk, r.rxFail)
	}
	if r.rxMBytes != nil {
		return fmt.Sprintf("%.2f MB", *r.rxMBytes)
	}
	return "-"
}

func packetsCell(ok, fail *int64) string {
	o, f := "0", "0"
	if ok != nil {
		o = formatInt64(*ok)
	}
	if fail != nil {
		f = formatInt64(*fail)
	}
	return o + "/" + f
}

// formatInt64 formats n with thousands separators (e.g. 1,234,567).
func formatInt64(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := n < 0
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	rem := len(s) % 3
	for i, ch := range s {
		if i > 0 && (i-rem)%3 == 0 {
			b.WriteRune(',')
		}
		b.WriteRune(ch)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// ─── Display-width helpers ───────────────────────────────────────────────────

// dispWidth returns the number of terminal columns a string occupies.
// East Asian wide characters (CJK etc.) count as 2 columns; all others as 1.
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		if isWide(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// isWide reports whether a rune occupies 2 terminal columns.
// Covers CJK Unified Ideographs, Hangul, full-width forms, and common ranges.
func isWide(r rune) bool {
	if r < 0x1100 {
		return false
	}
	switch {
	case r >= 0x1100 && r <= 0x115F: // Hangul Jamo
		return true
	case r >= 0x2E80 && r <= 0x303E: // CJK Radicals, Kangxi, etc.
		return true
	case r >= 0x3041 && r <= 0x33BF: // Hiragana, Katakana, Bopomofo, CJK compat
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK Extension A
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return true
	case r >= 0xA000 && r <= 0xA4CF: // Yi
		return true
	case r >= 0xAC00 && r <= 0xD7AF: // Hangul Syllables
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK Compatibility Ideographs
		return true
	case r >= 0xFE10 && r <= 0xFE1F: // Vertical forms
		return true
	case r >= 0xFE30 && r <= 0xFE4F: // CJK Compatibility Forms
		return true
	case r >= 0xFF01 && r <= 0xFF60: // Fullwidth Latin/Katakana
		return true
	case r >= 0xFFE0 && r <= 0xFFE6: // Fullwidth Signs
		return true
	case r >= 0x1F300 && r <= 0x1F9FF: // Emoji
		return true
	case r >= 0x20000 && r <= 0x2FFFD: // CJK Extension B+
		return true
	case r >= 0x30000 && r <= 0x3FFFD:
		return true
	}
	// Treat non-printable control chars as zero-width
	if !unicode.IsPrint(r) && r != utf8.RuneError {
		return false
	}
	return false
}

// padDisp pads s to exactly `width` display columns by appending spaces.
// For wide characters the actual byte length differs from display width.
func padDisp(s string, width int) string {
	dw := dispWidth(s)
	if dw >= width {
		return s
	}
	return s + strings.Repeat(" ", width-dw)
}

// truncateDisp truncates s to at most max display columns, appending "..." if shortened.
func truncateDisp(s string, maxW int) string {
	if maxW < 4 {
		maxW = 4
	}
	if dispWidth(s) <= maxW {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := 1
		if isWide(r) {
			rw = 2
		}
		if w+rw > maxW-3 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "..."
}

// ─── Table formatter ─────────────────────────────────────────────────────────

// FormatStatusTable builds an ASCII table from a slice of rows.
// When includeSwitchColumn is false the Switch column is omitted (per-switch view).
func FormatStatusTable(rows []statusRow, includeSwitchColumn bool) string {
	if len(rows) == 0 {
		return "(no ports)"
	}

	// Compute column widths in display columns (not bytes)
	colPort := dispWidth("Port")
	colLink := dispWidth("Link")
	colSpeed := dispWidth("Speed (Mbps)")
	colTx := dispWidth(headerTx)
	colRx := dispWidth(headerRx)
	for _, r := range rows {
		if w := dispWidth(portDisplay(r)); w > colPort {
			colPort = w
		}
		linkStr := "down"
		if r.linkUp {
			linkStr = "up"
		}
		if w := dispWidth(linkStr); w > colLink {
			colLink = w
		}
		speedStr := "-"
		if r.speedMbps != nil {
			speedStr = fmt.Sprintf("%d", *r.speedMbps)
		}
		if w := dispWidth(speedStr); w > colSpeed {
			colSpeed = w
		}
		if w := dispWidth(txCell(r)); w > colTx {
			colTx = w
		}
		if w := dispWidth(rxCell(r)); w > colRx {
			colRx = w
		}
	}
	colSwitch := 0
	if includeSwitchColumn {
		colSwitch = dispWidth("Switch")
		for _, r := range rows {
			if w := dispWidth(r.switchName); w > colSwitch {
				colSwitch = w
			}
		}
	}

	sep := makeSep(includeSwitchColumn, colSwitch, colPort, colLink, colSpeed, colTx, colRx)

	var sb strings.Builder
	sb.WriteString(sep + "\n")
	sb.WriteString(makeHead(includeSwitchColumn, colSwitch, colPort, colLink, colSpeed, colTx, colRx) + "\n")
	sb.WriteString(sep + "\n")

	for _, r := range rows {
		linkStr := "down"
		if r.linkUp {
			linkStr = "up"
		}
		speedStr := "-"
		if r.speedMbps != nil {
			speedStr = fmt.Sprintf("%d", *r.speedMbps)
		}

		var parts []string
		if includeSwitchColumn {
			parts = append(parts, " "+padDisp(r.switchName, colSwitch)+" ")
		}
		parts = append(parts,
			" "+padDisp(portDisplay(r), colPort)+" ",
			" "+padDisp(linkStr, colLink)+" ",
			" "+padDisp(speedStr, colSpeed)+" ",
			" "+padDisp(txCell(r), colTx)+" ",
			" "+padDisp(rxCell(r), colRx)+" ",
		)
		sb.WriteString("|" + strings.Join(parts, "|") + "|\n")
	}
	sb.WriteString(sep)
	return sb.String()
}

// FormatDNATRulesTable renders iKuai DNAT rules as a bordered table (one rule per row).
func FormatDNATRulesTable(rules []adapter.DNATRule) string {
	if len(rules) == 0 {
		return "(no rules)"
	}

	type dnatRow struct {
		id, status, proto, wan, lan, comment string
	}
	rows := make([]dnatRow, 0, len(rules))
	for _, rule := range rules {
		st := "OFF"
		if rule.Enabled() == "yes" {
			st = "ON"
		}
		proto := rule.String("protocol")
		if proto == "" {
			proto = "tcp/udp"
		}
		wan := rule.String("wan_port")
		if wan == "" {
			wan = "-"
		}
		lanHost := rule.String("lan_addr")
		lanPort := rule.String("lan_port")
		var lan string
		switch {
		case lanHost != "" && lanPort != "":
			lan = lanHost + ":" + lanPort
		case lanHost != "":
			lan = lanHost
		case lanPort != "":
			lan = ":" + lanPort
		default:
			lan = "-"
		}
		com := rule.Comment()
		if com == "" {
			com = "(no comment)"
		}
		com = truncateDisp(com, 56)
		rows = append(rows, dnatRow{
			id:      fmt.Sprintf("%d", rule.ID()),
			status:  st,
			proto:   proto,
			wan:     wan,
			lan:     lan,
			comment: com,
		})
	}

	colID := dispWidth("ID")
	colStatus := dispWidth("Status")
	colProto := dispWidth("Proto")
	colWan := dispWidth("WAN")
	colLan := dispWidth("LAN")
	colComment := dispWidth("Comment")
	for _, r := range rows {
		if w := dispWidth(r.id); w > colID {
			colID = w
		}
		if w := dispWidth(r.status); w > colStatus {
			colStatus = w
		}
		if w := dispWidth(r.proto); w > colProto {
			colProto = w
		}
		if w := dispWidth(r.wan); w > colWan {
			colWan = w
		}
		if w := dispWidth(r.lan); w > colLan {
			colLan = w
		}
		if w := dispWidth(r.comment); w > colComment {
			colComment = w
		}
	}

	sep := makeSepDNAT(colID, colStatus, colProto, colWan, colLan, colComment)

	var sb strings.Builder
	sb.WriteString(sep + "\n")
	sb.WriteString(makeHeadDNAT(colID, colStatus, colProto, colWan, colLan, colComment) + "\n")
	sb.WriteString(sep + "\n")

	for _, r := range rows {
		parts := []string{
			" " + padDisp(r.id, colID) + " ",
			" " + padDisp(r.status, colStatus) + " ",
			" " + padDisp(r.proto, colProto) + " ",
			" " + padDisp(r.wan, colWan) + " ",
			" " + padDisp(r.lan, colLan) + " ",
			" " + padDisp(r.comment, colComment) + " ",
		}
		sb.WriteString("|" + strings.Join(parts, "|") + "|\n")
	}
	sb.WriteString(sep)
	return sb.String()
}

func makeSepDNAT(colID, colStatus, colProto, colWan, colLan, colComment int) string {
	parts := []string{
		strings.Repeat("-", colID+2),
		strings.Repeat("-", colStatus+2),
		strings.Repeat("-", colProto+2),
		strings.Repeat("-", colWan+2),
		strings.Repeat("-", colLan+2),
		strings.Repeat("-", colComment+2),
	}
	return "+" + strings.Join(parts, "+") + "+"
}

func makeHeadDNAT(colID, colStatus, colProto, colWan, colLan, colComment int) string {
	parts := []string{
		" " + padDisp("ID", colID) + " ",
		" " + padDisp("Status", colStatus) + " ",
		" " + padDisp("Proto", colProto) + " ",
		" " + padDisp("WAN", colWan) + " ",
		" " + padDisp("LAN", colLan) + " ",
		" " + padDisp("Comment", colComment) + " ",
	}
	return "|" + strings.Join(parts, "|") + "|"
}

func makeSep(inclSwitch bool, colSwitch, colPort, colLink, colSpeed, colTx, colRx int) string {
	var parts []string
	if inclSwitch {
		parts = append(parts, strings.Repeat("-", colSwitch+2))
	}
	parts = append(parts,
		strings.Repeat("-", colPort+2),
		strings.Repeat("-", colLink+2),
		strings.Repeat("-", colSpeed+2),
		strings.Repeat("-", colTx+2),
		strings.Repeat("-", colRx+2),
	)
	return "+" + strings.Join(parts, "+") + "+"
}

func makeHead(inclSwitch bool, colSwitch, colPort, colLink, colSpeed, colTx, colRx int) string {
	var parts []string
	if inclSwitch {
		parts = append(parts, " "+padDisp("Switch", colSwitch)+" ")
	}
	parts = append(parts,
		" "+padDisp("Port", colPort)+" ",
		" "+padDisp("Link", colLink)+" ",
		" "+padDisp("Speed (Mbps)", colSpeed)+" ",
		" "+padDisp(headerTx, colTx)+" ",
		" "+padDisp(headerRx, colRx)+" ",
	)
	return "|" + strings.Join(parts, "|") + "|"
}
