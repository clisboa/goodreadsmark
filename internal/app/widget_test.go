package app

import (
	"encoding/json"
	"html"
	"strings"
	"testing"
)

func widgetEntry(book Book) string {
	id := strings.TrimPrefix(book.ID, "review:")
	link := "https://www.goodreads.com/review/show/" + id
	return `<div class="gr_custom_each_container_7"><div class="gr_custom_book_container_7"><a title="` + html.EscapeString(
		book.Title,
	) + `" href="` + link + `"><img src="https://example.invalid/cover.jpg"></a></div><div class="gr_custom_title_7"><a href="` + link + `">` + html.EscapeString(
		book.Title,
	) + `</a></div><div class="gr_custom_author_7">by <a href="https://www.goodreads.com/author/show/42.Writer">` + html.EscapeString(
		book.Author,
	) + `</a></div></div>`
}

func widgetHTML(fragment string) string {
	data, _ := json.Marshal(fragment)
	return "var widget_code = " + string(
		data,
	) + "\nvar widget_div = document.getElementById('gr_custom_widget_7'); if (!widget_div) { document.write(widget_code) }"
}

func widget(books ...Book) string {
	var fragment strings.Builder
	fragment.WriteString(`<div class="gr_custom_container_7">`)
	for _, book := range books {
		fragment.WriteString(widgetEntry(book))
	}
	fragment.WriteString(`</div>`)
	return widgetHTML(fragment.String())
}

func TestWidgetMetadata(t *testing.T) {
	full := Book{ID: "review:987", Title: `Dúné & "Friends" (Series, #1)`, Author: "René O’Writer"}
	entry := widgetEntry(full)
	entry = strings.Replace(entry, ">Dúné &amp; &#34;Friends&#34; (Series, #1)</a>", ">Dúné</a>", 1)
	books, err := ParseWidget([]byte(widgetHTML(`<div class="gr_custom_container_7">` + entry + `</div>`)))
	if err != nil || len(books) != 1 || books[0] != full {
		t.Fatal(books, err)
	}
	// Images are optional and must never supply an identity.
	entry = strings.Replace(entry, `<div class="gr_custom_book_container_7">`, `<div class="unused_cover">`, 1)
	books, err = ParseWidget([]byte(widgetHTML(`<div class="gr_custom_container_7">` + entry + `</div>`)))
	if err != nil || len(books) != 1 || books[0].ID != "review:987" || books[0].Title != "Dúné" {
		t.Fatal(books, err)
	}
	books, err = ParseWidget([]byte(widget(full, full)))
	if err != nil || len(books) != 1 {
		t.Fatal(books, err)
	}
}

func TestWidgetLiteral(t *testing.T) {
	fragment := `<div class="gr_custom_container_7"></div>`
	single := `var widget_code = '<div class=\"gr_custom_container_7\">\n<\/div>'
var widget_div = null; document.write(widget_code)`
	for _, input := range []string{widgetHTML(fragment), single} {
		books, err := ParseWidget([]byte(input))
		if err != nil || len(books) != 0 {
			t.Fatal(books, err)
		}
	}
	// Valid JavaScript surrogate pairs and escaped apostrophes are data, never code.
	escaped := strings.Replace(
		widget(Book{ID: "review:1", Title: "A 🐉's book", Author: "Writer"}),
		"🐉",
		`\uD83D\uDC09`,
		1,
	)
	books, err := ParseWidget([]byte(escaped))
	if err != nil || len(books) != 1 || books[0].Title != "A 🐉's book" {
		t.Fatal(books, err)
	}
}

func TestWidgetRejectsUnsafeOrMalformedData(t *testing.T) {
	entry := widgetEntry(Book{ID: "review:987", Title: "A Book", Author: "A Writer"})
	wrap := func(s string) string { return widgetHTML(`<div class="gr_custom_container_7">` + s + `</div>`) }
	cases := []struct{ name, input string }{
		{"login", "<html><title>Sign in</title></html>"},
		{"challenge", "<html>Verify you are human</html>"},
		{"no_root", widgetHTML(entry)},
		{"unclosed", widgetHTML(`<div class="gr_custom_container_7">` + entry)},
		{"missing_id", wrap(strings.ReplaceAll(entry, "/review/show/987", "/review/show/0"))},
		{"wrong_host", wrap(strings.ReplaceAll(entry, "www.goodreads.com/review", "evil.invalid/review"))},
		{
			"unrelated_cover",
			wrap(strings.Replace(entry, "https://www.goodreads.com/review/show/987", "https://evil.invalid/other", 1)),
		},
		{"conflicting_id", wrap(strings.Replace(entry, "/review/show/987", "/review/show/986", 1))},
		{
			"conflicting_duplicate",
			widget(Book{ID: "review:1", Title: "A", Author: "W"}, Book{ID: "review:1", Title: "B", Author: "W"}),
		},
		{"missing_author", wrap(strings.ReplaceAll(entry, ">A Writer</a>", "></a>"))},
		{
			"missing_title",
			wrap(strings.ReplaceAll(strings.ReplaceAll(entry, `title="A Book"`, `title=""`), ">A Book</a>", "></a>")),
		},
		{"expression", `var widget_code = '<div></div>' + steal(); var widget_div = null;`},
		{"unterminated", `var widget_code = '<div>`},
		{"unsupported_escape", `var widget_code = '\q'; var widget_div = null;`},
		{"unpaired_surrogate", `var widget_code = '\uD800'; var widget_div = null;`},
		{"utf8", string([]byte{0xff})},
		{"depth", wrap(strings.Repeat("<div>", 70) + strings.Repeat("</div>", 70))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseWidget([]byte(tc.input)); err == nil {
				t.Fatal("invalid widget accepted")
			}
		})
	}
}
