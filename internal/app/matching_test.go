package app

import (
	"encoding/json"
	"testing"
)

func releaseOf(title string) Release {
	return Release{
		"source":    json.RawMessage(`"test"`),
		"source_id": json.RawMessage(`"abc"`),
		"title": rawJSON(
			title,
		),
		"format": json.RawMessage(`"epub"`),
		"extra":  json.RawMessage(`{"author":"Frank Herbert"}`),
	}
}

func rawJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func dune() Book {
	return Book{ID: "1", Title: "Dune", Author: "Frank Herbert", ISBN13: "9780441172719"}
}

func TestMatchingRejectsFalsePositives(t *testing.T) {
	cases := []struct{ name, title string }{
		{"sequel", "Dune Messiah"},
		{"prefix", "Children of Dune"},
		{"number", "Dune 2"},
		{"part", "Dune Part II"},
		{"summary", "Dune Summary"},
		{"study", "Dune Study Guide"},
		{"graphic", "Dune Graphic Novel"},
		{"collection", "Dune Complete Collection"},
		{"pack", "Dune Books 1-6"},
		{"trilogy", "Dune Trilogy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelectRelease(
				dune(),
				[]Release{releaseOf(tc.title)},
				MatchOptions{Formats: []string{"epub"}},
			); got != nil {
				t.Fatal("wrong book accepted")
			}
		})
	}
}

func TestMatchingFormatsAndAuthors(t *testing.T) {
	cases := []struct {
		name, key string
		value     any
		accept    bool
	}{
		{"exact", "title", "Dune", true},
		{"missing_format", "format", nil, false},
		{"pdf", "format", "pdf", false},
		{"audio", "content_type", "audiobook", false},
		{"missing_source", "source", "", false},
		{"multi", "multi_book", true, false},
		{"no_author", "extra", map[string]any{}, false},
		{"wrong_author", "extra", map[string]any{"author": "Robert Herbert"}, false},
		{"surname", "extra", map[string]any{"author": "Herbert"}, false},
		{"isbn", "extra", map[string]any{"isbn13": "9780441172719"}, true},
		{"filename", "title", "Frank Herbert - Dune.epub", true},
		{"series", "title", "Dune (Dune, #1)", true},
		{"bad_filename", "extra", map[string]any{"author": "Frank Herbert", "filename": "Dune.pdf"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := releaseOf("Dune")
			release[tc.key] = rawJSON(tc.value)
			got := SelectRelease(dune(), []Release{release}, MatchOptions{Formats: []string{"epub"}})
			if (got != nil) != tc.accept {
				t.Fatal("unexpected matching result")
			}
		})
	}
}

func TestFormatOrderingUnicodeAndExclusion(t *testing.T) {
	epub, kepub := releaseOf("Dune"), releaseOf("Dune.kepub.epub")
	got := SelectRelease(dune(), []Release{epub, kepub}, MatchOptions{Formats: []string{"kepub", "epub"}})
	if text(got, "title") != "Dune.kepub.epub" {
		t.Fatal("format preference ignored")
	}
	if SelectRelease(dune(), []Release{kepub}, MatchOptions{Formats: []string{"epub"}}) != nil {
		t.Fatal("KEPUB relabeled")
	}
	if Normalize("Été") != Normalize("ETE") {
		t.Fatal("accent normalization")
	}
	if SearchTitle("Dune (Dune, #1)") != "Dune" {
		t.Fatal("series annotation retained")
	}
	if SelectRelease(
		dune(),
		[]Release{epub},
		MatchOptions{Formats: []string{"epub"}, Excluded: map[Identity]bool{{Source: "test", ID: "abc"}: true}},
	) != nil {
		t.Fatal("failed release selected")
	}
}
