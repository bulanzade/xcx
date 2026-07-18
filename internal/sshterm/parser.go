package sshterm

import (
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// Parser feeds raw PTY bytes into a Screen, decoding UTF-8 and the subset of
// xterm control sequences the Screen understands. Unknown escape sequences are
// consumed and dropped so they can't desync subsequent text.
//
// State machine (a trimmed-down VT100/xterm):
//
//	ground  -- ESC(0x1b) -->  escape
//	escape  -- '['        -->  csi
//	escape  -- other      -->  ground (drop; could be a 1-byte C1 we ignore)
//	csi     -- 0x30..0x3f -->  csi (param bytes: digits and ';')
//	csi     -- 0x40..0x7e -->  ground (final byte: dispatch)
type Parser struct {
	screen *Screen
	style  Style // current SGR style applied to printed runes

	state   parseState
	params  []int  // parsed CSI parameters
	cur     int    // current parameter accumulator
	hasNum  bool   // whether cur holds any digit
	interm  []byte // intermediate bytes 0x20..0x2f (mostly ignored)
	private byte   // private parameter prefix 0x3c..0x3f (0 = none), e.g. '?' for DEC modes
	utfbuf  []byte // leftover incomplete UTF-8 sequence

	// respond is invoked when the remote program sends a query that the
	// terminal must answer (DSR cursor-position report, device attributes,
	// OSC color queries). The bytes it receives are a complete response to
	// write back to the PTY. nil = queries are silently dropped.
	respond func(b []byte)

	// oscBuf accumulates the payload of an in-progress OSC sequence.
	oscBuf []byte

	bracketedPaste    atomic.Bool
	applicationCursor atomic.Bool
}

// Responder is the interface for sending response bytes back to the remote
// process (e.g. DSR/DA replies).
type Responder interface {
	Respond(b []byte)
}

// SetResponder wires a response callback for query sequences. The Terminal
// connects this to its stdin writer so replies reach the remote program.
func (p *Parser) SetResponder(r func(b []byte)) { p.respond = r }

// BracketedPaste reports whether the remote program currently requested
// bracketed paste mode via CSI ? 2004 h/l.
func (p *Parser) BracketedPaste() bool { return p.bracketedPaste.Load() }

// ApplicationCursor reports whether the remote program enabled DECCKM
// application cursor keys (CSI ? 1 h). When on, arrow keys must be sent in the
// application encoding (ESC O A/B/C/D), not the normal-cursor encoding
// (ESC [ A/B/C/D). vim/less/man turn this on so their arrow keys work; a bare
// shell leaves it off. It also gates our local Shift+arrow scroll-back: in
// application mode those keys belong to the remote program (bubbletea decodes
// ESC O A/B as KeyShiftUp/Down), so we must forward them instead of scrolling.
func (p *Parser) ApplicationCursor() bool { return p.applicationCursor.Load() }

type parseState int

const (
	stGround parseState = iota
	stEscape
	stCSI
	// stEscapeIntermediate handles ESC I...I F sequences (e.g. ESC ( B to
	// select G0 = US-ASCII, ESC ) 0 for DEC special graphics). The intermediate
	// bytes (0x20..0x2f) are consumed until a final byte (0x30..0x7e) ends the
	// sequence. Without this state, the final byte (e.g. 'B') leaked as text —
	// the stray "B" seen on every line when running `top`.
	stEscapeIntermediate
	// stOSC handles Operating System Commands (ESC ] ...). The payload runs
	// until BEL (0x07) or ST (ESC \). Without consuming it, vim's color queries
	// (ESC ] 10 ; ? BEL, ESC ] 11 ; ? BEL) leaked as the visible text "10;?11;?".
	stOSC
	stOSCESC // inside an OSC, saw an ESC: next byte must be '\' to form ST
)

// NewParser creates a Parser that applies bytes to screen. The initial style
// is the default (Fg/Bg = -1 = terminal default), NOT the Go zero value
// (which is 0 = black). Without this, plain text before any SGR reset would
// render as black-on-black.
func NewParser(screen *Screen) *Parser {
	return &Parser{screen: screen, style: Style{Fg: -1, Bg: -1}}
}

// Write feeds a chunk of bytes through the state machine.
func (p *Parser) Write(b []byte) {
	// First, append to any pending UTF-8 fragment and decode runes.
	if len(p.utfbuf) > 0 {
		p.utfbuf = append(p.utfbuf, b...)
		dec := p.decodeRunes(p.utfbuf)
		b = dec.remaining
		p.utfbuf = nil
		_ = dec // runes already applied inside decodeRunes
		// Re-enter: process the fully-decoded prefix is already done; process b below.
	}

	for i := 0; i < len(b); {
		rb, size := utf8.DecodeRune(b[i:])
		if rb == utf8.RuneError && size == 1 {
			// possible incomplete sequence at the tail
			if utf8.FullRune(b[i:]) {
				// genuinely invalid byte; emit replacement and move on
				p.handleRune('�')
				i++
				continue
			}
			// incomplete: stash and wait for more
			p.utfbuf = append(p.utfbuf, b[i:]...)
			return
		}
		p.handleRune(rb)
		i += size
	}
}

// decodeRunes applies all complete runes in buf and returns the leftover tail.
// We keep this as a helper used only by Write's fast path.
func (p *Parser) decodeRunes(buf []byte) struct{ remaining []byte } {
	for i := 0; i < len(buf); {
		rb, size := utf8.DecodeRune(buf[i:])
		if rb == utf8.RuneError && size == 1 {
			if !utf8.FullRune(buf[i:]) {
				return struct{ remaining []byte }{buf[i:]}
			}
			p.handleRune('�')
			i++
			continue
		}
		p.handleRune(rb)
		i += size
	}
	return struct{ remaining []byte }{nil}
}

// handleRune dispatches a single decoded rune (control or printable) according
// to the current parser state.
func (p *Parser) handleRune(r rune) {
	switch p.state {
	case stGround:
		p.handleGround(r)
	case stEscape:
		p.handleEscape(r)
	case stEscapeIntermediate:
		p.handleEscapeIntermediate(r)
	case stCSI:
		p.handleCSI(r)
	case stOSC:
		p.handleOSC(r)
	case stOSCESC:
		// We saw ESC inside an OSC; '\' completes ST and ends the sequence.
		// Anything else also ends it (treat ESC as a hard reset to escape).
		if r == '\\' {
			p.finishOSC()
		} else {
			p.oscBuf = p.oscBuf[:0]
			p.state = stEscape
			p.handleEscape(r)
		}
	}
}

func (p *Parser) handleGround(r rune) {
	switch r {
	case 0x1b: // ESC
		p.state = stEscape
	case '\r':
		p.screen.CarriageReturn()
	case '\n', '\v', '\f': // LF / VT / FF all act as line feed
		p.screen.LineFeed()
	case '\b': // BS
		p.screen.Backspace()
	case '\t':
		p.screen.Tab()
	case 0x07: // BEL — ignore
	default:
		if r >= 0x20 {
			p.screen.Print(r, p.style)
		}
		// other C0 controls ignored
	}
}

func (p *Parser) handleEscape(r rune) {
	switch {
	case r == '[':
		p.enterCSI()
	case r == ']':
		// OSC: collect the payload until BEL or ST (ESC \). The body is
		// dispatched in handleOSC; we must consume ALL of it, otherwise vim's
		// color queries ("]10;?" "]11;?") leak as visible text.
		p.oscBuf = p.oscBuf[:0]
		p.state = stOSC
	case r >= 0x20 && r <= 0x2f:
		// Intermediate byte (e.g. '(' or ')' of ESC ( B / ESC ) 0). Collect
		// intermediates and wait for the final byte.
		p.interm = append(p.interm, byte(r))
		p.state = stEscapeIntermediate
	default:
		// Other escape sequences (e.g. ESC c reset, ESC = ) are ignored.
		p.state = stGround
	}
}

// handleEscapeIntermediate consumes the final byte of an ESC I...I F sequence
// (charset selection and similar). We don't act on these for the MVP, but we
// must consume the final byte so it doesn't print as text.
func (p *Parser) handleEscapeIntermediate(r rune) {
	if r >= 0x30 && r <= 0x7e {
		// final byte: sequence complete, drop it (charset selection is a no-op
		// for our purposes since we always render UTF-8).
		p.interm = p.interm[:0]
		p.state = stGround
		return
	}
	if r >= 0x20 && r <= 0x2f {
		// additional intermediate byte
		p.interm = append(p.interm, byte(r))
		return
	}
	// unexpected: bail to ground
	p.interm = p.interm[:0]
	p.state = stGround
}

// handleOSC accumulates an OSC payload byte by byte. The sequence ends on BEL
// (0x07) or ST (ESC \). On completion it dispatches the payload (color queries
// get a response; everything else is dropped). Critically, the body never
// reaches the screen, which fixes the "10;?11;?" leak from vim's color queries.
func (p *Parser) handleOSC(r rune) {
	switch {
	case r == 0x07: // BEL terminator
		p.finishOSC()
	case r == 0x1b: // ESC: start of ST (ESC \)
		p.state = stOSCESC
	default:
		p.oscBuf = utf8.AppendRune(p.oscBuf, r)
	}
}

// finishOSC dispatches the accumulated OSC payload and returns to ground.
func (p *Parser) finishOSC() {
	body := p.oscBuf
	p.oscBuf = p.oscBuf[:0]
	p.state = stGround
	if dir := currentDirFromOSC(string(body)); dir != "" {
		p.screen.SetCurrentDir(dir)
	}
	if p.respond == nil {
		return
	}
	// OSC color query: "10;?" (fg) or "11;?" (bg) -> reply with a default
	// color so apps like vim that read the terminal palette are satisfied.
	// We answer "rgb:rrrr/gggg/bbbb" using the classic defaults.
	switch {
	case hasOSCParam(body, "10;?"):
		p.respond([]byte("\x1b]10;rgb:cccc/cccc/cccc\x1b\\"))
	case hasOSCParam(body, "11;?"):
		p.respond([]byte("\x1b]11;rgb:0000/0000/0000\x1b\\"))
	case hasOSCParam(body, "12;?"):
		p.respond([]byte("\x1b]12;rgb:0000/0000/0000\x1b\\"))
	}
}

// currentDirFromOSC extracts shell working-directory reports. OSC 7 is the
// standard form. The additional forms cover common shell integrations, while
// OSC 0/2 handles Ubuntu's default Bash title ("user@host: ~/path").
func currentDirFromOSC(body string) string {
	switch {
	case strings.HasPrefix(body, "7;"):
		u, err := url.Parse(strings.TrimPrefix(body, "7;"))
		if err != nil || u.Scheme != "file" {
			return ""
		}
		return unescapeReportedDir(u.EscapedPath())
	case strings.HasPrefix(body, "9;9;"):
		return unescapeReportedDir(strings.TrimPrefix(body, "9;9;"))
	case strings.HasPrefix(body, "1337;CurrentDir="):
		return unescapeReportedDir(strings.TrimPrefix(body, "1337;CurrentDir="))
	case strings.HasPrefix(body, "633;P;Cwd="):
		return unescapeReportedDir(strings.TrimPrefix(body, "633;P;Cwd="))
	case strings.HasPrefix(body, "0;") || strings.HasPrefix(body, "2;"):
		return currentDirFromShellTitle(body[2:])
	default:
		return ""
	}
}

func unescapeReportedDir(raw string) string {
	dir, err := url.PathUnescape(raw)
	if err != nil {
		return ""
	}
	return validRemoteDir(dir)
}

func validRemoteDir(dir string) string {
	if dir == "" || strings.ContainsRune(dir, '\x00') || !looksLikeRemoteDir(dir) {
		return ""
	}
	return dir
}

func looksLikeRemoteDir(dir string) bool {
	if strings.HasPrefix(dir, "/") || dir == "~" || strings.HasPrefix(dir, "~/") {
		return true
	}
	return len(dir) >= 3 && ((dir[0] >= 'A' && dir[0] <= 'Z') || (dir[0] >= 'a' && dir[0] <= 'z')) &&
		dir[1] == ':' && (dir[2] == '/' || dir[2] == '\\')
}

func currentDirFromShellTitle(title string) string {
	at := strings.IndexByte(title, '@')
	if at < 0 {
		return ""
	}
	afterUser := title[at+1:]
	separator := strings.Index(afterUser, ": ")
	if separator < 0 {
		return ""
	}
	return validRemoteDir(strings.TrimSpace(afterUser[separator+2:]))
}

// hasOSCParam reports whether the OSC payload body begins with prefix.
func hasOSCParam(body []byte, prefix string) bool {
	if len(body) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if body[i] != prefix[i] {
			return false
		}
	}
	return true
}

func (p *Parser) enterCSI() {
	p.state = stCSI
	p.params = p.params[:0]
	p.cur = 0
	p.hasNum = false
	p.interm = p.interm[:0]
	p.private = 0
}

func (p *Parser) handleCSI(r rune) {
	switch {
	case r >= '0' && r <= '9':
		p.cur = p.cur*10 + int(r-'0')
		p.hasNum = true
	case r == ';':
		p.params = append(p.params, p.curOrDefault(0))
		p.cur = 0
		p.hasNum = false
	case r >= 0x3c && r <= 0x3f: // private parameter prefix: <=>? (e.g. CSI ?2004 h)
		// These mark a "private" sequence (DEC modes, bracketed paste, etc.).
		// We record the marker so dispatch knows it's private, then keep
		// consuming params until the final byte.
		p.private = byte(r)
	case r >= 0x20 && r <= 0x2f: // intermediate bytes
		p.interm = append(p.interm, byte(r))
	case r >= 0x40 && r <= 0x7e: // final byte
		// flush last param if any digits were seen since last ';'
		if p.hasNum || len(p.params) > 0 {
			p.params = append(p.params, p.curOrDefault(0))
		}
		p.dispatchCSI(r)
		p.state = stGround
	default:
		// unexpected inside CSI; reset to ground
		p.state = stGround
	}
}

// curOrDefault returns p.cur if digits were seen, else def.
func (p *Parser) curOrDefault(def int) int {
	if p.hasNum {
		return p.cur
	}
	return def
}

// param returns the i-th CSI parameter (0-based), or def if absent.
func (p *Parser) param(i, def int) int {
	if i < len(p.params) {
		return p.params[i]
	}
	return def
}

func (p *Parser) dispatchCSI(final rune) {
	// Handle query/response sequences first — some use private markers
	// (DA2 = CSI > c) so they must be matched before the generic private drop.
	if p.respond != nil {
		switch {
		case final == 'n' && p.private == 0:
			// Device Status Report.
			switch p.param(0, 0) {
			case 5:
				// "report terminal status" -> terminal OK.
				p.respond([]byte("\x1b[0n"))
				return
			case 6:
				// "report cursor position" -> CSI Pl ; Pc R (1-based).
				r, c := p.screen.Cursor()
				p.respond([]byte("\x1b[" + strconv.Itoa(r+1) + ";" + strconv.Itoa(c+1) + "R"))
				return
			}
		case final == 'c':
			// Device Attributes.
			switch p.private {
			case 0:
				// Primary DA: claim VT220 (62) with ANSI color (22) capability.
				p.respond([]byte("\x1b[?62;22c"))
				return
			case '>':
				// Secondary DA: terminal type 0, firmware version 0.
				p.respond([]byte("\x1b[>0;0;0c"))
				return
			}
		}
	}

	// Private sequences (CSI ? ... h/l). Act on ?1/?47/?1047/?1049/?2004;
	// drop the rest (e.g. ?25 cursor visibility, ?6 origin mode) fully so their
	// digits don't leak as text.
	if p.private != 0 {
		if p.private == '?' && len(p.params) > 0 && (final == 'h' || final == 'l') {
			enabled := final == 'h'
			switch p.params[0] {
			case 1: // DECCKM: application cursor keys on/off.
				p.applicationCursor.Store(enabled)
			case 47, 1047, 1049: // alternate screen (?1049 cursor save/restore not implemented).
				if enabled {
					p.screen.EnterAltScreen()
				} else {
					p.screen.LeaveAltScreen()
				}
			case 2004: // bracketed paste mode.
				p.bracketedPaste.Store(enabled)
			}
		}
		return
	}
	switch final {
	case 'H', 'f': // cursor position: CSI Pl ; Pc H (1-based)
		row := p.param(0, 1)
		col := p.param(1, 1)
		p.screen.SetCursor(row-1, col-1)
	case 'A': // up
		p.screen.MoveCursor(-p.param(0, 1), 0)
	case 'B': // down
		p.screen.MoveCursor(p.param(0, 1), 0)
	case 'C': // right
		p.screen.MoveCursor(0, p.param(0, 1))
	case 'D': // left
		p.screen.MoveCursor(0, -p.param(0, 1))
	case 'G', '`': // cursor to column
		curRow, _ := p.screen.Cursor()
		p.screen.SetCursor(curRow, p.param(0, 1)-1)
	case 'd': // cursor to row
		_, curCol := p.screen.Cursor()
		p.screen.SetCursor(p.param(0, 1)-1, curCol)
	case 'J': // erase display
		p.screen.ClearScreen(p.param(0, 0))
	case 'K': // erase line
		p.screen.ClearLine(p.param(0, 0))
	case 'P': // delete chars (DCH)
		p.screen.DeleteChars(p.param(0, 1))
	case '@': // insert chars (ICH)
		p.screen.InsertChars(p.param(0, 1))
	case 'X': // erase chars (ECH)
		p.screen.EraseChars(p.param(0, 1))
	case 'r': // DECSTBM scroll region: CSI Pt ; Pb r (1-based, inclusive).
		p.screen.SetScrollRegion(p.param(0, 1)-1, p.param(1, p.screen.Rows())-1)
	case 'L': // insert lines (IL)
		p.screen.InsertLines(p.param(0, 1))
	case 'M': // delete lines (DL)
		p.screen.DeleteLines(p.param(0, 1))
	case 'S': // scroll up (SU)
		p.screen.ScrollRegionUp(p.param(0, 1))
	case 'T': // scroll down (SD)
		p.screen.ScrollRegionDown(p.param(0, 1))
	case 'm': // SGR
		p.applySGR()
	default:
		// ignore: h/l (modes), s/u (cursor save/restore), etc.
	}
}

// applySGR interprets CSI ... m parameters into p.style.
func (p *Parser) applySGR() {
	if len(p.params) == 0 {
		p.params = append(p.params, 0)
	}
	for i := 0; i < len(p.params); i++ {
		v := p.params[i]
		switch {
		case v == 0:
			p.style = Style{Fg: -1, Bg: -1}
		case v == 1:
			p.style.Bold = true
		case v == 2:
			p.style.Dim = true
		case v == 3:
			p.style.Italic = true
		case v == 4:
			p.style.Under = true
		case v == 7:
			p.style.Rev = true
		case v == 22:
			p.style.Bold = false
			p.style.Dim = false
		case v == 23:
			p.style.Italic = false
		case v == 24:
			p.style.Under = false
		case v == 27:
			p.style.Rev = false
		case v >= 30 && v <= 37:
			p.style.Fg = int8(v - 30)
		case v == 39:
			p.style.Fg = -1
		case v >= 40 && v <= 47:
			p.style.Bg = int8(v - 40)
		case v == 49:
			p.style.Bg = -1
		case v == 38 || v == 48:
			// 256-color / truecolor: skip the next param(s)
			if i+1 < len(p.params) {
				mode := p.params[i+1]
				if mode == 5 && i+2 < len(p.params) {
					i += 2
				} else if mode == 2 && i+4 < len(p.params) {
					i += 4
				}
			}
		}
	}
}
