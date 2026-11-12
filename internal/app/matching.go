package app

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var (
	initialsRE    = regexp.MustCompile(`\b([A-Za-z]\.\s*){2,}`)
	seriesRE      = regexp.MustCompile(`(?i)\s*\([^)]*(?:#\s*|book\s+)(\d+(?:\.\d+)?)[^)]*\)\s*$`)
	decorationRE  = regexp.MustCompile(`(?i)\s*[\[(](?:epub|kepub|english|portuguese|en|pt)[\])]`)
	epubSuffixRE  = regexp.MustCompile(`(?i)\.(?:kepub\.)?epub$`)
	wrongSuffixRE = regexp.MustCompile(`(?i)\.(?:pdf|mobi|azw3?|zip|rar|mp3|m4b)$`)
	integerRE     = regexp.MustCompile(`^-?[0-9]+$`)
)

func Normalize(value string) string {
	value = initialsRE.ReplaceAllStringFunc(value, func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) {
				return r
			}
			return -1
		}, s)
	})
	value = norm.NFKD.String(value)
	value = strings.Map(func(r rune) rune {
		if norm.NFKD.PropertiesString(string(r)).CCC() != 0 {
			return -1
		}
		return r
	}, value)
	value = cases.Fold().String(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return r
		}
		return ' '
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func SearchTitle(value string) string { return strings.TrimSpace(seriesRE.ReplaceAllString(value, "")) }

func topText(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func extra(release Release) Release {
	result := Release{}
	if err := json.Unmarshal(release["extra"], &result); err != nil || result == nil {
		return Release{}
	}
	return result
}

func truthy(raw json.RawMessage) bool {
	value := string(bytes.TrimSpace(raw))
	switch value {
	case "", "null", "false", "0", `""`, "[]", "{}":
		return false
	default:
		return true
	}
}

func text(release Release, key string) string {
	value := release[key]
	if !truthy(value) {
		value = extra(release)[key]
	}
	return topText(value)
}

func identity(release Release) (Identity, bool) {
	source := strings.TrimSpace(topText(release["source"]))
	identifier := topText(release["source_id"])
	if identifier == "" && integerRE.Match(release["source_id"]) {
		identifier = string(release["source_id"])
	}
	identifier = strings.TrimSpace(identifier)
	return Identity{Source: source, ID: identifier}, source != "" && identifier != ""
}

func releaseFormat(release Release) string {
	declared := strings.TrimLeft(strings.ToLower(strings.TrimSpace(text(release, "format"))), ".")
	extension := strings.TrimLeft(strings.ToLower(strings.TrimSpace(text(release, "extension"))), ".")
	if declared == "kepub.epub" {
		declared = "kepub"
	}
	if extension == "kepub.epub" {
		extension = "kepub"
	}
	for _, value := range []string{declared, extension} {
		if value != "" && value != "epub" && value != "kepub" {
			return ""
		}
	}
	filename := text(release, "filename")
	if filename == "" {
		filename = text(release, "title")
	}
	if wrongSuffixRE.MatchString(filename) {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(filename), ".kepub.epub") {
		return "kepub"
	}
	if declared != "" && extension != "" && declared != extension {
		return ""
	}
	if declared != "" {
		return declared
	}
	if extension != "" {
		return extension
	}
	if strings.HasSuffix(strings.ToLower(filename), ".epub") {
		return "epub"
	}
	return ""
}

func stripTitle(value string) (string, string) {
	value = strings.TrimSpace(decorationRE.ReplaceAllString(value, ""))
	value = epubSuffixRE.ReplaceAllString(value, "")
	volume := ""
	if match := seriesRE.FindStringSubmatch(value); len(match) > 1 {
		volume = match[1]
		value = SearchTitle(value)
	}
	return Normalize(value), volume
}

func authorEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	left, right := strings.Fields(Normalize(a)), strings.Fields(Normalize(b))
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func isbnMatch(book Book, release Release) bool {
	for _, name := range []string{"isbn", "isbn13", "isbn_13", "isbn_10"} {
		found := cleanISBN(text(release, name))
		if found != "" && (found == book.ISBN || found == book.ISBN13) {
			return true
		}
	}
	return false
}

func matchBook(book Book, release Release) bool {
	title := text(release, "title")
	if title == "" {
		return false
	}
	wanted, wantedVolume := stripTitle(book.Title)
	got, gotVolume := stripTitle(title)
	if wantedVolume != "" && gotVolume != "" && wantedVolume != gotVolume {
		return false
	}
	additional := extra(release)
	for _, values := range []Release{release, additional} {
		if truthy(values["multi_book"]) {
			return false
		}
		if plan, ok := values["book_plan"]; ok && string(plan) != "null" {
			return false
		}
	}
	if strings.Contains(strings.ToLower(text(release, "content_type")), "audio") {
		return false
	}
	wantedWords := " " + Normalize(book.Title) + " "
	gotWords := " " + Normalize(title) + " "
	for _, word := range []string{"summary", "summaries", "analysis", "workbook", "abridged", "adaptation", "dramatized", "omnibus", "trilogy", "megapack", "collection", "boxset"} {
		if strings.Contains(gotWords, " "+word+" ") && !strings.Contains(wantedWords, " "+word+" ") {
			return false
		}
	}
	for _, phrase := range []string{"study guide", "graphic novel", "box set", "complete works", "audiobook"} {
		if strings.Contains(gotWords, phrase) && !strings.Contains(wantedWords, phrase) {
			return false
		}
	}
	author := text(release, "author")
	authorMatches := authorEqual(book.Author, author)
	if author != "" && !authorMatches {
		return false
	}
	wantedAuthor := Normalize(book.Author)
	variants := []string{wantedAuthor}
	if words := strings.Fields(wantedAuthor); len(words) > 0 {
		reversed := append([]string{words[len(words)-1]}, words[:len(words)-1]...)
		variants = append(variants, strings.Join(reversed, " "))
	}
	embedded := false
	for _, variant := range variants {
		if variant == "" {
			continue
		}
		switch {
		case strings.HasPrefix(got, variant+" "):
			got = strings.TrimPrefix(got, variant+" ")
			embedded = true
		case strings.HasSuffix(got, " "+variant):
			got = strings.TrimSuffix(got, " "+variant)
			embedded = true
		}
	}
	if wanted == "" || got != wanted {
		return false
	}
	return authorMatches || embedded || isbnMatch(book, release)
}

type MatchOptions struct {
	Formats, Languages []string
	Excluded           map[Identity]bool
}

func SelectRelease(book Book, releases []Release, opts MatchOptions) Release {
	languages := map[string]bool{}
	for _, code := range opts.Languages {
		part, _, _ := strings.Cut(code, "-")
		languages[part] = true
	}
	names := map[string]string{
		"english":    "en",
		"portuguese": "pt",
		"french":     "fr",
		"german":     "de",
		"spanish":    "es",
		"italian":    "it",
		"dutch":      "nl",
	}
	var chosen Release
	best := len(opts.Formats)
	for _, release := range releases {
		key, ok := identity(release)
		if !ok || opts.Excluded[key] {
			continue
		}
		index := slices.Index(opts.Formats, releaseFormat(release))
		if index < 0 || index >= best {
			continue
		}
		language := strings.ToLower(strings.TrimSpace(text(release, "language")))
		if code, ok := names[language]; ok {
			language = code
		}
		language, _, _ = strings.Cut(language, "-")
		if len(languages) > 0 && !languages[language] {
			continue
		}
		if matchBook(book, release) {
			chosen = release
			best = index
		}
	}
	return chosen
}
