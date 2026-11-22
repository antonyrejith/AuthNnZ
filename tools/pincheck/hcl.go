package main

import "strings"

// A small, brace-aware reader for the parts of HCL that carry versions:
// blocks with labels, attributes, string literals and object values (the
// inline form `aws = { source = "…", version = "…" }`). Other expressions are
// skipped. It is not a full HCL parser and does not need to be: anything it
// cannot read as a literal is reported as not exactly pinned.

type tokKind int

const (
	tIdent tokKind = iota
	tString
	tLBrace
	tRBrace
	tAssign // = or :
	tComma
	tOpen  // ( or [
	tClose // ) or ]
	tNewline
	tOther
)

type token struct {
	kind tokKind
	text string
	line int
}

type hclLexer struct {
	src  string
	pos  int
	line int
	out  []token
}

func lexHCL(src string) []token {
	lx := &hclLexer{src: src, line: 1}
	for lx.pos < len(lx.src) {
		lx.next()
	}
	return lx.out
}

func (lx *hclLexer) emit(k tokKind, text string) {
	lx.out = append(lx.out, token{k, text, lx.line})
}

func (lx *hclLexer) next() {
	c := lx.src[lx.pos]
	rest := lx.src[lx.pos:]
	switch {
	case c == '\n':
		lx.emit(tNewline, "")
		lx.line++
		lx.pos++
	case c == ' ' || c == '\t' || c == '\r':
		lx.pos++
	case c == '#' || strings.HasPrefix(rest, "//"):
		end := strings.IndexByte(rest, '\n')
		if end < 0 {
			end = len(rest)
		}
		lx.pos += end
	case strings.HasPrefix(rest, "/*"):
		end := strings.Index(rest[2:], "*/")
		if end < 0 {
			end = len(rest) - 4
		}
		lx.line += strings.Count(rest[:end+4], "\n")
		lx.pos += end + 4
	case c == '"':
		lx.str()
	case strings.HasPrefix(rest, "<<"):
		lx.heredoc()
	case isIdentByte(c):
		end := 0
		for end < len(rest) && isIdentByte(rest[end]) {
			end++
		}
		lx.emit(tIdent, rest[:end])
		lx.pos += end
	default:
		lx.emit(punct(c), string(c))
		lx.pos++
	}
}

func punct(c byte) tokKind {
	switch c {
	case '{':
		return tLBrace
	case '}':
		return tRBrace
	case '=', ':':
		return tAssign
	case ',':
		return tComma
	case '(', '[':
		return tOpen
	case ')', ']':
		return tClose
	default:
		return tOther
	}
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '-' || c == '.' || c == '*' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// str reads a quoted string; "${ … }" templates may contain quotes.
func (lx *hclLexer) str() {
	line := lx.line
	var b strings.Builder
	i := lx.pos + 1
	depth := 0
	for i < len(lx.src) {
		c := lx.src[i]
		switch {
		case c == '\\' && i+1 < len(lx.src):
			b.WriteByte(lx.src[i+1])
			i += 2
			continue
		case c == '"' && depth == 0:
			lx.out = append(lx.out, token{tString, b.String(), line})
			lx.pos = i + 1
			return
		case strings.HasPrefix(lx.src[i:], "${"):
			depth++
		case c == '}' && depth > 0:
			depth--
		case c == '\n':
			lx.line++
		}
		b.WriteByte(c)
		i++
	}
	lx.out = append(lx.out, token{tString, b.String(), line})
	lx.pos = len(lx.src)
}

// heredoc skips <<EOF … EOF and <<-EOF … EOF, emitting it as a non-literal.
func (lx *hclLexer) heredoc() {
	start := lx.pos + 2
	if strings.HasPrefix(lx.src[start:], "-") {
		start++
	}
	nl := strings.IndexByte(lx.src[start:], '\n')
	if nl < 0 {
		lx.pos = len(lx.src)
		return
	}
	marker := strings.TrimSpace(lx.src[start : start+nl])
	lx.emit(tOther, "<<")
	// Each body line's newline is consumed here; the terminator line's
	// newline is left for next() to emit.
	for i := start + nl + 1; i < len(lx.src); {
		lx.line++
		end := strings.IndexByte(lx.src[i:], '\n')
		if end < 0 {
			end = len(lx.src) - i
		}
		if strings.TrimSpace(lx.src[i:i+end]) == marker {
			lx.pos = i + end
			return
		}
		i += end + 1
	}
	lx.pos = len(lx.src)
}

type hclValue struct {
	line int
	str  *string    // set for a string literal
	obj  []hclEntry // set for an object
}

func (v hclValue) field(name string) (hclValue, bool) {
	for _, e := range v.obj {
		if e.name == name {
			return e.val, true
		}
	}
	return hclValue{}, false
}

type hclEntry struct {
	name string
	val  hclValue
}

type hclBlock struct {
	typ    string
	labels []string
	line   int
	attrs  []hclEntry
	blocks []*hclBlock
}

func (b *hclBlock) attr(name string) (hclValue, bool) {
	return hclValue{obj: b.attrs}.field(name)
}

type hclParser struct {
	toks []token
	pos  int
}

func parseHCL(toks []token) *hclBlock {
	p := &hclParser{toks: toks}
	root := &hclBlock{}
	p.body(root, false)
	return root
}

func (p *hclParser) peek() (token, bool) {
	if p.pos >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.pos], true
}

// body reads attributes and blocks into b until EOF or, when inBraces, the
// closing brace.
func (p *hclParser) body(b *hclBlock, inBraces bool) {
	for {
		t, ok := p.peek()
		if !ok {
			return
		}
		switch t.kind {
		case tNewline, tComma:
			p.pos++
		case tRBrace:
			p.pos++
			if inBraces {
				return
			}
		case tIdent, tString:
			p.item(b, t)
		case tLBrace, tAssign, tOpen, tClose, tOther:
			p.pos++ // not the start of an item
		}
	}
}

func (p *hclParser) item(b *hclBlock, key token) {
	p.pos++
	t, ok := p.peek()
	if ok && t.kind == tAssign {
		p.pos++
		b.attrs = append(b.attrs, hclEntry{key.text, p.value(key.line)})
		return
	}
	blk := &hclBlock{typ: key.text, line: key.line}
	for ok && (t.kind == tString || t.kind == tIdent) {
		blk.labels = append(blk.labels, t.text)
		p.pos++
		t, ok = p.peek()
	}
	if ok && t.kind == tLBrace {
		p.pos++
		p.body(blk, true)
		b.blocks = append(b.blocks, blk)
	}
}

func (p *hclParser) value(line int) hclValue {
	t, ok := p.peek()
	if !ok {
		return hclValue{line: line}
	}
	if t.kind == tLBrace {
		p.pos++
		obj := &hclBlock{}
		p.body(obj, true)
		return hclValue{line: t.line, obj: append([]hclEntry{}, obj.attrs...)}
	}
	if t.kind == tString && p.endsValue(p.pos+1) {
		p.pos++
		s := t.text
		return hclValue{line: t.line, str: &s}
	}
	p.skipExpr()
	return hclValue{line: t.line}
}

func (p *hclParser) endsValue(i int) bool {
	if i >= len(p.toks) {
		return true
	}
	k := p.toks[i].kind
	return k == tNewline || k == tComma || k == tRBrace
}

// skipExpr skips an expression up to the end of its line, a comma or the
// enclosing closing brace.
func (p *hclParser) skipExpr() {
	depth := 0
	for {
		t, ok := p.peek()
		if !ok {
			return
		}
		switch t.kind {
		case tOpen, tLBrace:
			depth++
		case tClose:
			depth--
		case tRBrace:
			if depth == 0 {
				return
			}
			depth--
		case tNewline, tComma:
			if depth == 0 {
				return
			}
		case tIdent, tString, tAssign, tOther:
		}
		p.pos++
	}
}
