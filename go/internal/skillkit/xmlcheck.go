package skillkit

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// xmlRoot is xml.etree.ElementTree.parse(path).getroot().tag for the
// well-formedness checks design.verify_artifact needs.  Errors carry
// expat's message texts and its "line L, column C" position (columns in
// bytes, as pyexpat reports them for UTF-8 input) for the cases a broken
// figure typically hits: empty files, unclosed or mismatched tags, junk
// around the root, bad tokens, undefined entities, bad character
// references, unbound prefixes and duplicate attributes.  Documents in
// other encodings than UTF-8 are not supported.
type xmlError struct {
	msg       string
	line, col int
}

func (e *xmlError) Error() string { return fmt.Sprintf("%s: line %d, column %d", e.msg, e.line, e.col) }

type xmlParser struct {
	src      []byte
	pos      int
	doctype  bool // a DOCTYPE with an external subset (SYSTEM/PUBLIC)
	entities map[string]bool
	stack    []xmlElem
}

type xmlElem struct {
	name string
	ns   map[string]string
}

func (p *xmlParser) at(pos int, msg string) error {
	line, col := 1, 0
	for i := 0; i < pos && i < len(p.src); i++ {
		switch p.src[i] {
		case '\n':
			line, col = line+1, 0
		case '\r':
			if i+1 < len(p.src) && p.src[i+1] == '\n' {
				i++
			}
			line, col = line+1, 0
		default:
			col++
		}
	}
	return &xmlError{msg, line, col}
}

func isNameStart(r rune) bool {
	return r == '_' || r == ':' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r >= 0xC0
}

func isNameChar(r rune) bool {
	return isNameStart(r) || r == '-' || r == '.' || (r >= '0' && r <= '9') || r == 0xB7
}

func validChar(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
}

func isXMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func (p *xmlParser) rune() (rune, int) {
	r, n := utf8.DecodeRune(p.src[p.pos:])
	return r, n
}

func (p *xmlParser) name() (string, bool) {
	start := p.pos
	r, n := p.rune()
	if p.pos >= len(p.src) || !isNameStart(r) || r == utf8.RuneError {
		return "", false
	}
	p.pos += n
	for p.pos < len(p.src) {
		r, n = p.rune()
		if !isNameChar(r) || r == utf8.RuneError {
			break
		}
		p.pos += n
	}
	return string(p.src[start:p.pos]), true
}

func (p *xmlParser) skipSpace() {
	for p.pos < len(p.src) && isXMLSpace(p.src[p.pos]) {
		p.pos++
	}
}

// reference parses &...; at p.pos (inside text or an attribute value).
func (p *xmlParser) reference() error {
	start := p.pos
	p.pos++
	if p.pos < len(p.src) && p.src[p.pos] == '#' {
		p.pos++
		hex := p.pos < len(p.src) && p.src[p.pos] == 'x'
		if hex {
			p.pos++
		}
		ds := p.pos
		for p.pos < len(p.src) && p.src[p.pos] != ';' {
			c := p.src[p.pos]
			ok := c >= '0' && c <= '9' || hex && (c|0x20 >= 'a' && c|0x20 <= 'f')
			if !ok {
				return p.at(p.pos, "not well-formed (invalid token)")
			}
			p.pos++
		}
		if p.pos >= len(p.src) {
			return p.at(start, "unclosed token")
		}
		if p.pos == ds {
			return p.at(p.pos, "not well-formed (invalid token)")
		}
		base := 10
		if hex {
			base = 16
		}
		n, err := strconv.ParseInt(string(p.src[ds:p.pos]), base, 64)
		if err != nil || n > 0x10FFFF || !validChar(rune(n)) {
			return p.at(start, "reference to invalid character number")
		}
		p.pos++
		return nil
	}
	name, ok := p.name()
	if !ok {
		return p.at(p.pos, "not well-formed (invalid token)")
	}
	if p.pos >= len(p.src) {
		return p.at(start, "unclosed token")
	}
	if p.src[p.pos] != ';' {
		return p.at(p.pos, "not well-formed (invalid token)")
	}
	p.pos++
	switch name {
	case "amp", "lt", "gt", "quot", "apos":
		return nil
	}
	if p.entities[name] {
		return nil
	}
	if p.doctype {
		e := p.at(p.pos, "")
		xe := e.(*xmlError)
		return &xmlError{"undefined entity &" + name + ";", xe.line, xe.col}
	}
	return p.at(start, "undefined entity")
}

func resolveNS(stack []xmlElem, prefix string) (string, bool) {
	if prefix == "xml" {
		return "http://www.w3.org/XML/1998/namespace", true
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if uri, ok := stack[i].ns[prefix]; ok {
			return uri, true
		}
	}
	return "", prefix == ""
}

// startTag parses <name attrs...> at p.pos; returns the expanded tag.
func (p *xmlParser) startTag() (string, bool, error) {
	start := p.pos
	p.pos++
	name, ok := p.name()
	if !ok {
		if p.pos >= len(p.src) {
			return "", false, p.at(start, "unclosed token")
		}
		return "", false, p.at(p.pos, "not well-formed (invalid token)")
	}
	type attr struct {
		name string
		pos  int
	}
	attrs := []attr{}
	ns := map[string]string{}
	empty := false
	for {
		hadSpace := p.pos < len(p.src) && isXMLSpace(p.src[p.pos])
		p.skipSpace()
		if p.pos >= len(p.src) {
			return "", false, p.at(start, "unclosed token")
		}
		c := p.src[p.pos]
		if c == '>' {
			p.pos++
			break
		}
		if c == '/' {
			if p.pos+1 >= len(p.src) {
				return "", false, p.at(start, "unclosed token")
			}
			if p.src[p.pos+1] != '>' {
				return "", false, p.at(p.pos+1, "not well-formed (invalid token)")
			}
			p.pos += 2
			empty = true
			break
		}
		if !hadSpace {
			return "", false, p.at(p.pos, "not well-formed (invalid token)")
		}
		apos := p.pos
		an, ok := p.name()
		if !ok {
			return "", false, p.at(p.pos, "not well-formed (invalid token)")
		}
		p.skipSpace()
		if p.pos >= len(p.src) {
			return "", false, p.at(start, "unclosed token")
		}
		if p.src[p.pos] != '=' {
			return "", false, p.at(p.pos, "not well-formed (invalid token)")
		}
		p.pos++
		p.skipSpace()
		if p.pos >= len(p.src) {
			return "", false, p.at(start, "unclosed token")
		}
		q := p.src[p.pos]
		if q != '"' && q != '\'' {
			return "", false, p.at(p.pos, "not well-formed (invalid token)")
		}
		p.pos++
		vs := p.pos
		for {
			if p.pos >= len(p.src) {
				return "", false, p.at(start, "unclosed token")
			}
			c := p.src[p.pos]
			if c == q {
				break
			}
			if c == '<' {
				return "", false, p.at(p.pos, "not well-formed (invalid token)")
			}
			if c == '&' {
				if err := p.reference(); err != nil {
					return "", false, err
				}
				continue
			}
			r, n := p.rune()
			if r == utf8.RuneError && n <= 1 || !validChar(r) {
				return "", false, p.at(p.pos, "not well-formed (invalid token)")
			}
			p.pos += n
		}
		value := string(p.src[vs:p.pos])
		p.pos++
		for _, a := range attrs {
			if a.name == an {
				return "", false, p.at(apos, "duplicate attribute")
			}
		}
		attrs = append(attrs, attr{an, apos})
		if an == "xmlns" {
			ns[""] = value
		} else if strings.HasPrefix(an, "xmlns:") {
			ns[an[6:]] = value
		}
	}
	elem := xmlElem{name: name, ns: ns}
	stack := append(p.stack, elem)
	prefix, local := "", name
	if i := strings.IndexByte(name, ':'); i >= 0 {
		prefix, local = name[:i], name[i+1:]
	}
	uri, ok := resolveNS(stack, prefix)
	if !ok {
		return "", false, p.at(start, "unbound prefix")
	}
	for _, a := range attrs {
		if i := strings.IndexByte(a.name, ':'); i >= 0 && a.name[:i] != "xmlns" {
			if _, ok := resolveNS(stack, a.name[:i]); !ok {
				return "", false, p.at(start, "unbound prefix")
			}
		}
	}
	if !empty {
		p.stack = stack
	}
	tag := local
	if uri != "" {
		tag = "{" + uri + "}" + local
	}
	return tag, empty, nil
}

// misc skips a comment, PI or (prolog only) DOCTYPE at p.pos.
func (p *xmlParser) misc(prolog bool) (bool, error) {
	start := p.pos
	rest := p.src[p.pos:]
	switch {
	case bytes.HasPrefix(rest, []byte("<!--")):
		end := bytes.Index(rest[4:], []byte("--"))
		if end < 0 {
			return true, p.at(start, "unclosed token")
		}
		if 4+end+2 >= len(rest) {
			return true, p.at(start, "unclosed token")
		}
		if rest[4+end+2] != '>' {
			return true, p.at(start+4+end+2, "not well-formed (invalid token)")
		}
		p.pos += 4 + end + 3
		return true, nil
	case bytes.HasPrefix(rest, []byte("<?")):
		end := bytes.Index(rest, []byte("?>"))
		if end < 0 {
			return true, p.at(start, "unclosed token")
		}
		target := rest[2:]
		if bytes.HasPrefix(bytes.ToLower(target), []byte("xml")) && (len(target) == 3 || isXMLSpace(target[3]) || target[3] == '?') && start != 0 {
			return true, p.at(start, "XML or text declaration not at start of entity")
		}
		p.pos += end + 2
		return true, nil
	case prolog && bytes.HasPrefix(rest, []byte("<!DOCTYPE")):
		depth := 0
		for i := 9; i < len(rest); i++ {
			switch rest[i] {
			case '[':
				depth++
			case ']':
				depth--
			case '>':
				if depth <= 0 {
					decl := rest[:i+1]
					head := decl
					if j := bytes.IndexByte(decl, '['); j >= 0 {
						head = decl[:j]
						for _, part := range bytes.Split(decl[j:], []byte("<!ENTITY"))[1:] {
							fields := bytes.Fields(part)
							if len(fields) > 0 && string(fields[0]) != "%" {
								if p.entities == nil {
									p.entities = map[string]bool{}
								}
								p.entities[string(fields[0])] = true
							}
						}
					}
					p.doctype = bytes.Contains(head, []byte(" SYSTEM")) || bytes.Contains(head, []byte(" PUBLIC"))
					p.pos += i + 1
					return true, nil
				}
			}
		}
		return true, p.at(start, "unclosed token")
	}
	return false, nil
}

func xmlRoot(data []byte) (string, error) {
	p := &xmlParser{src: data}
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		p.pos = 3
	}
	root := ""
	// prolog
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return "", p.at(p.pos, "no element found")
		}
		if p.src[p.pos] != '<' {
			// expat's prolog tokenizer: a run of name characters (or one
			// other character) followed by '<' is an invalid token there,
			// anything else a syntax error at the run's start.
			start := p.pos
			r, n := p.rune()
			if r == utf8.RuneError && n <= 1 {
				return "", p.at(p.pos, "not well-formed (invalid token)")
			}
			p.pos += n
			if isNameChar(r) {
				for p.pos < len(p.src) {
					r, n = p.rune()
					if !isNameChar(r) || r == utf8.RuneError {
						break
					}
					p.pos += n
				}
			}
			if p.pos < len(p.src) && p.src[p.pos] == '<' {
				return "", p.at(p.pos, "not well-formed (invalid token)")
			}
			return "", p.at(start, "syntax error")
		}
		handled, err := p.misc(true)
		if err != nil {
			return "", err
		}
		if handled {
			continue
		}
		if p.pos+1 >= len(p.src) {
			return "", p.at(p.pos, "unclosed token")
		}
		tag, empty, err := p.startTag()
		if err != nil {
			return "", err
		}
		root = tag
		if empty {
			return root, p.epilog()
		}
		break
	}
	// content
	for len(p.stack) > 0 {
		if p.pos >= len(p.src) {
			return "", p.at(p.pos, "no element found")
		}
		c := p.src[p.pos]
		switch {
		case c == '&':
			if err := p.reference(); err != nil {
				return "", err
			}
		case c == '<':
			rest := p.src[p.pos:]
			if bytes.HasPrefix(rest, []byte("<![CDATA[")) {
				end := bytes.Index(rest, []byte("]]>"))
				if end < 0 {
					return "", p.at(len(p.src), "unclosed CDATA section")
				}
				p.pos += end + 3
				continue
			}
			if bytes.HasPrefix(rest, []byte("</")) {
				start := p.pos
				p.pos += 2
				ns := p.pos
				name, ok := p.name()
				if !ok {
					if p.pos >= len(p.src) {
						return "", p.at(start, "unclosed token")
					}
					return "", p.at(p.pos, "not well-formed (invalid token)")
				}
				p.skipSpace()
				if p.pos >= len(p.src) {
					return "", p.at(start, "unclosed token")
				}
				if p.src[p.pos] != '>' {
					return "", p.at(p.pos, "not well-formed (invalid token)")
				}
				if name != p.stack[len(p.stack)-1].name {
					return "", p.at(ns, "mismatched tag")
				}
				p.pos++
				p.stack = p.stack[:len(p.stack)-1]
				continue
			}
			handled, err := p.misc(false)
			if err != nil {
				return "", err
			}
			if handled {
				continue
			}
			if bytes.HasPrefix(rest, []byte("<!")) {
				return "", p.at(p.pos+1, "not well-formed (invalid token)")
			}
			if p.pos+1 >= len(p.src) {
				return "", p.at(p.pos, "unclosed token")
			}
			if _, _, err := p.startTag(); err != nil {
				return "", err
			}
		default:
			r, n := p.rune()
			if r == utf8.RuneError && n <= 1 || !validChar(r) {
				return "", p.at(p.pos, "not well-formed (invalid token)")
			}
			if c == ']' && bytes.HasPrefix(p.src[p.pos:], []byte("]]>")) {
				return "", p.at(p.pos+2, "not well-formed (invalid token)")
			}
			p.pos += n
		}
	}
	return root, p.epilog()
}

func (p *xmlParser) epilog() error {
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return nil
		}
		if p.src[p.pos] == '<' {
			handled, err := p.misc(false)
			if err != nil {
				return err
			}
			if handled {
				continue
			}
		}
		return p.at(p.pos, "junk after document element")
	}
}
