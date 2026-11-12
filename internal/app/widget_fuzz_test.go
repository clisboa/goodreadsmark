package app

import (
	"strings"
	"testing"
)

func FuzzParseWidget(f *testing.F) {
	f.Add(widget(Book{ID: "review:987", Title: "A dragon's 🐉 book", Author: "René Writer"}))
	f.Add(widget())
	f.Add(`var widget_code = '\uD800'; var widget_div = null;`)
	f.Add("<html>Sign in</html>")
	f.Fuzz(func(t *testing.T, input string) {
		books, err := ParseWidget([]byte(input))
		if err != nil {
			return
		}
		if len(books) > 100 {
			t.Fatal("widget limit exceeded")
		}
		seen := map[string]bool{}
		for _, book := range books {
			if !strings.HasPrefix(book.ID, "review:") || !validBookID(book.ID) || book.Title == "" ||
				book.Author == "" ||
				seen[book.ID] {
				t.Fatal("invalid parsed metadata")
			}
			seen[book.ID] = true
		}
	})
}
