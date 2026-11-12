package app

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/net/html"
)

var (
	widgetAssignment = regexp.MustCompile(`^\s*var\s+widget_code\s*=\s*`)
	reviewPath       = regexp.MustCompile(`^/review/show/([1-9][0-9]*)/?$`)
)

// ParseWidget reads a literal HTML payload. It never evaluates the surrounding JavaScript.
func ParseWidget(data []byte) ([]Book, error) {
	invalid := problem("Goodreads returned an invalid or unrecognized public widget")
	if len(data) > maxResponseBytes || !utf8.Valid(data) {
		return nil, invalid
	}
	match := widgetAssignment.FindIndex(data)
	if match == nil {
		return nil, invalid
	}
	fragment, err := widgetLiteral(string(data[match[1]:]))
	if err != nil || !validWidgetHTML(fragment) {
		return nil, invalid
	}
	doc, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		return nil, invalid
	}
	roots := widgetNodes(doc, "gr_custom_container_")
	if len(roots) != 1 {
		return nil, invalid
	}
	entries := widgetNodes(roots[0], "gr_custom_each_container_")
	if len(entries) > 100 {
		return nil, invalid
	}
	books := []Book{}
	seen := map[string]Book{}
	for _, entry := range entries {
		book, err := widgetBook(entry)
		if err != nil {
			return nil, invalid
		}
		if previous, ok := seen[book.ID]; ok {
			if previous != book {
				return nil, invalid
			}
			continue
		}
		seen[book.ID] = book
		books = append(books, book)
	}
	return books, nil
}

func widgetLiteral(input string) (string, error) {
	invalid := problem("invalid widget string literal")
	if len(input) == 0 || (input[0] != '\'' && input[0] != '"') {
		return "", invalid
	}
	quote := input[0]
	var out strings.Builder
	for i := 1; i < len(input); i++ {
		ch := input[i]
		if ch == quote {
			tail := strings.TrimSpace(input[i+1:])
			tail = strings.TrimSpace(strings.TrimPrefix(tail, ";"))
			if !strings.HasPrefix(tail, "var widget_div") {
				return "", invalid
			}
			return out.String(), nil
		}
		if ch != '\\' {
			if ch < 32 {
				return "", invalid
			}
			out.WriteByte(ch)
			continue
		}
		i++
		if i >= len(input) {
			return "", invalid
		}
		switch input[i] {
		case '\\', '/', '\'', '"':
			out.WriteByte(input[i])
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case 'b':
			out.WriteByte('\b')
		case 'f':
			out.WriteByte('\f')
		case 'u', 'x':
			width := 4
			if input[i] == 'x' {
				width = 2
			}
			if i+width >= len(input) {
				return "", invalid
			}
			value, err := strconv.ParseUint(input[i+1:i+1+width], 16, 16)
			if err != nil {
				return "", invalid
			}
			i += width
			r := rune(value)
			if utf16.IsSurrogate(r) {
				if r < 0xD800 || r > 0xDBFF || i+6 >= len(input) || input[i+1:i+3] != `\u` {
					return "", invalid
				}
				low, err := strconv.ParseUint(input[i+3:i+7], 16, 16)
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return "", invalid
				}
				r = utf16.DecodeRune(r, rune(low))
				i += 6
			}
			out.WriteRune(r)
		default:
			return "", invalid
		}
	}
	return "", invalid
}

// Tokenize first to reject truncated div containers before HTML's error recovery repairs them.
func validWidgetHTML(fragment string) bool {
	if strings.ContainsFunc(fragment, func(r rune) bool { return r < 32 && r != '\n' && r != '\r' && r != '\t' }) {
		return false
	}
	tokenizer := html.NewTokenizer(strings.NewReader(fragment))
	depth, tokens := 0, 0
	for {
		kind := tokenizer.Next()
		tokens++
		if tokens > 100000 {
			return false
		}
		if kind == html.ErrorToken {
			return errors.Is(tokenizer.Err(), io.EOF) && depth == 0
		}
		if kind != html.StartTagToken && kind != html.EndTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		name, _ := tokenizer.TagName()
		if !bytes.Equal(name, []byte("div")) {
			continue
		}
		switch kind {
		case html.StartTagToken:
			depth++
		case html.EndTagToken:
			depth--
		case html.SelfClosingTagToken:
			return false
		}
		if depth < 0 || depth > 64 {
			return false
		}
	}
}

func nodeAttr(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}

func widgetNodes(root *html.Node, prefix string) []*html.Node {
	nodes := []*html.Node{}
	for node := range root.Descendants() {
		if node.Type != html.ElementNode || node.Data != "div" {
			continue
		}
		for _, class := range strings.Fields(nodeAttr(node, "class")) {
			if suffix, ok := strings.CutPrefix(class, prefix); ok && numericID.MatchString(suffix) {
				nodes = append(nodes, node)
				break
			}
		}
	}
	return nodes
}

func nodeText(root *html.Node) string {
	var out strings.Builder
	for node := range root.Descendants() {
		if node.Type == html.TextNode {
			out.WriteString(node.Data)
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func goodreadsLink(value string) (*url.URL, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawPath != "" {
		return nil, false
	}
	relative := parsed.Scheme == "" && parsed.Host == "" && strings.HasPrefix(parsed.Path, "/")
	absolute := parsed.Scheme == "https" && (parsed.Host == "www.goodreads.com" || parsed.Host == "goodreads.com")
	return parsed, relative || absolute
}

func widgetBook(entry *html.Node) (Book, error) {
	book := Book{}
	invalid := problem("widget entry is missing valid review identity, title or author")
	// Every review link in this entry must refer to the same review, including the cover link.
	for node := range entry.Descendants() {
		if node.Type != html.ElementNode || node.Data != "a" {
			continue
		}
		href := nodeAttr(node, "href")
		parsed, ok := goodreadsLink(href)
		if !strings.Contains(href, "/review/show/") {
			continue
		}
		if !ok {
			return book, invalid
		}
		match := reviewPath.FindStringSubmatch(parsed.Path)
		if match == nil {
			return book, invalid
		}
		id := "review:" + match[1]
		if book.ID != "" && book.ID != id {
			return book, invalid
		}
		book.ID = id
	}
	for _, field := range []struct {
		prefix string
		target *string
	}{
		{prefix: "gr_custom_title_", target: &book.Title},
		{prefix: "gr_custom_author_", target: &book.Author},
	} {
		containers := widgetNodes(entry, field.prefix)
		if len(containers) != 1 {
			return book, invalid
		}
		links := 0
		for node := range containers[0].Descendants() {
			if node.Type != html.ElementNode || node.Data != "a" {
				continue
			}
			parsed, ok := goodreadsLink(nodeAttr(node, "href"))
			if !ok {
				return book, invalid
			}
			if field.prefix == "gr_custom_title_" && !reviewPath.MatchString(parsed.Path) {
				return book, invalid
			}
			if field.prefix == "gr_custom_author_" && !strings.HasPrefix(parsed.Path, "/author/show/") {
				return book, invalid
			}
			*field.target = nodeText(node)
			links++
		}
		if links != 1 || *field.target == "" {
			return book, invalid
		}
	}
	covers := widgetNodes(entry, "gr_custom_book_container_")
	if len(covers) > 1 {
		return book, invalid
	}
	for _, cover := range covers {
		links := 0
		for node := range cover.Descendants() {
			if node.Type != html.ElementNode || node.Data != "a" {
				continue
			}
			parsed, ok := goodreadsLink(nodeAttr(node, "href"))
			if !ok {
				return book, invalid
			}
			match := reviewPath.FindStringSubmatch(parsed.Path)
			if match == nil || "review:"+match[1] != book.ID {
				return book, invalid
			}
			links++
			if links > 1 {
				return book, invalid
			}
			full := strings.Join(strings.Fields(nodeAttr(node, "title")), " ")
			if full != "" {
				book.Title = full
			}
		}
	}
	if book.ID == "" || book.Title == "" || book.Author == "" {
		return book, invalid
	}
	return book, nil
}
