package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
)

const (
	historyPages    = 50
	historyPageSize = 100
)

var queueStatuses = map[string]bool{
	"queued":      true,
	"resolving":   true,
	"locating":    true,
	"downloading": true,
	"complete":    true,
	"error":       true,
	"cancelled":   true,
}

func activeStatus(status string) bool {
	return status == "queued" || status == "resolving" || status == "locating" || status == "downloading"
}

type Shelfmark struct {
	Config Config
	Client *http.Client
}

func (s *Shelfmark) jsonRequest(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	headers := http.Header{
		"Accept": {"application/json"}, "Content-Type": {"application/json"},
	}
	if s.Config.apiKey != "" {
		headers.Set("X-Api-Key", s.Config.apiKey)
	}
	client := *s.Client
	client.Jar = nil // Use the configured API key or intentional anonymous access, never a shared browser session.
	data, err := request(ctx, requestOptions{
		Client:  &client,
		Method:  method,
		URL:     s.Config.BaseURL + path,
		Service: "Shelfmark",
		Body:    body,
		Headers: headers,
	})
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, &RemoteError{Message: "Shelfmark returned invalid JSON", Ambiguous: method == "POST"}
	}
	return data, nil
}

func (s *Shelfmark) Search(ctx context.Context, book Book) (SearchResult, error) {
	result := SearchResult{Releases: []Release{}, Warnings: []string{}}
	query := url.Values{
		"provider":     {"manual"},
		"book_id":      {book.ID},
		"title":        {SearchTitle(book.Title)},
		"author":       {book.Author},
		"content_type": {"ebook"},
	}
	if len(s.Config.Languages) > 0 {
		query.Set("languages", strings.Join(s.Config.Languages, ","))
	}
	data, err := s.jsonRequest(ctx, "GET", "/api/releases?"+query.Encode(), nil)
	if err != nil {
		return result, err
	}
	var parsed struct {
		Releases []json.RawMessage `json:"releases"`
		Book     struct {
			Library map[string]string `json:"library"`
		} `json:"book"`
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || parsed.Releases == nil {
		return result, &RemoteError{Message: "Shelfmark release response has an invalid structure"}
	}
	result.Owned = parsed.Book.Library["ebook"] == "owned"
	if truthy(parsed.Errors) {
		result.Warnings = append(
			result.Warnings,
			"some Shelfmark sources failed during search; remaining results were considered",
		)
	}
	for _, raw := range parsed.Releases {
		release := Release{}
		if json.Unmarshal(raw, &release) == nil && release != nil {
			result.Releases = append(result.Releases, release)
		}
	}
	return result, nil
}

func (s *Shelfmark) Submit(ctx context.Context, release Release) error {
	key, ok := identity(release)
	if !ok {
		return problem("cannot submit a release without its source identity")
	}
	payload := Release{}
	for name, value := range release {
		payload[name] = value
	}
	source, err := json.Marshal(key.Source)
	if err != nil {
		return err
	}
	identifier, err := json.Marshal(key.ID)
	if err != nil {
		return err
	}
	payload["source"] = source
	payload["source_id"] = identifier
	payload["content_type"] = json.RawMessage(`"ebook"`)
	body, err := json.Marshal(payload)
	if err != nil {
		return problem("release payload is invalid")
	}
	data, err := s.jsonRequest(ctx, "POST", "/api/releases/download", body)
	if err != nil {
		return err
	}
	var parsed struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || parsed.Status != "queued" {
		return &RemoteError{Message: "Shelfmark did not confirm the submission outcome", Ambiguous: true}
	}
	return nil
}

func object(raw json.RawMessage) (Release, bool) {
	result := Release{}
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, false
	}
	return result, true
}

func scalarID(raw json.RawMessage) string {
	if value := topText(raw); value != "" {
		return value
	}
	if integerRE.Match(raw) {
		return string(raw)
	}
	return ""
}

func addedTime(raw json.RawMessage) *float64 {
	var value float64
	if json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) || string(raw) == "null" {
		return nil
	}
	return &value
}

func statusRows(raw json.RawMessage) (map[Identity]Observation, error) {
	result := map[Identity]Observation{}
	buckets, ok := object(raw)
	if !ok {
		return nil, &RemoteError{Message: "Shelfmark status response has an invalid structure"}
	}
	for status, bucket := range buckets {
		if !queueStatuses[status] {
			continue
		}
		rows, ok := object(bucket)
		if !ok {
			return nil, &RemoteError{Message: "Shelfmark status bucket has an invalid structure"}
		}
		for id, raw := range rows {
			payload, ok := object(raw)
			if !ok {
				continue
			}
			source := topText(payload["source"])
			if source == "" {
				continue
			}
			actual := scalarID(payload["id"])
			if actual != "" && actual != id {
				continue
			}
			key := Identity{Source: source, ID: id}
			result[key] = Observation{Identity: key, Status: status, AddedTime: addedTime(payload["added_time"])}
		}
	}
	return result, nil
}

func (s *Shelfmark) Observations(ctx context.Context) (Activity, error) {
	activity := Activity{Items: map[Identity]Observation{}, Warnings: []string{}}
	raw, err := s.jsonRequest(ctx, "GET", "/api/status", nil)
	if err != nil {
		return activity, err
	}
	live, err := statusRows(raw)
	if err != nil {
		return activity, err
	}
	snapshot := map[Identity]Observation{}
	raw, err = s.jsonRequest(ctx, "GET", "/api/activity/snapshot", nil)
	if err == nil {
		doc, ok := object(raw)
		if ok {
			snapshot, err = statusRows(doc["status"])
		} else {
			err = &RemoteError{Message: "Shelfmark snapshot has an invalid structure"}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return activity, ctx.Err()
		}
		if globalRemote(err) {
			return activity, err
		}
		snapshot = map[Identity]Observation{}
		activity.Warnings = append(
			activity.Warnings,
			"persisted Shelfmark snapshot is unavailable; missing outcomes remain unknown",
		)
	}
	seen := map[string]bool{}
	exhausted := true
	for page := range historyPages {
		if err := ctx.Err(); err != nil {
			return activity, err
		}
		path := fmt.Sprintf("/api/activity/history?limit=%d&offset=%d", historyPageSize, page*historyPageSize)
		raw, err := s.jsonRequest(ctx, "GET", path, nil)
		entries := []json.RawMessage{}
		if err == nil {
			if json.Unmarshal(raw, &entries) != nil || entries == nil {
				err = &RemoteError{Message: "Shelfmark history has an invalid structure"}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return activity, ctx.Err()
			}
			if globalRemote(err) {
				return activity, err
			}
			activity.Warnings = append(
				activity.Warnings,
				"dismissed Shelfmark history is unavailable; missing outcomes remain unknown",
			)
			exhausted = false
			break
		}
		added := 0
		for _, entry := range entries {
			row, ok := object(entry)
			if !ok {
				continue
			}
			itemKey := topText(row["item_key"])
			if itemKey == "" || seen[itemKey] {
				continue
			}
			seen[itemKey] = true
			added++
			if topText(row["item_type"]) != "download" {
				continue
			}
			wrapped, ok := object(row["snapshot"])
			if !ok {
				continue
			}
			download, ok := object(wrapped["download"])
			if !ok {
				continue
			}
			source, id := topText(download["source"]), scalarID(download["id"])
			status := topText(row["final_status"])
			if status == "active" {
				status = "queued"
			}
			if source == "" || id == "" || !queueStatuses[status] {
				continue
			}
			key := Identity{Source: source, ID: id}
			if _, exists := activity.Items[key]; !exists {
				activity.Items[key] = Observation{
					Identity:  key,
					Status:    status,
					AddedTime: addedTime(download["added_time"]),
				}
			}
		}
		if len(entries) < historyPageSize {
			exhausted = false
			break
		}
		if added == 0 {
			activity.Warnings = append(
				activity.Warnings,
				"Shelfmark history pagination repeated; missing outcomes remain unknown",
			)
			exhausted = false
			break
		}
	}
	if exhausted {
		activity.Warnings = append(
			activity.Warnings,
			"Shelfmark history page limit reached; missing outcomes remain unknown",
		)
	}
	for key, observation := range snapshot {
		activity.Items[key] = observation
	}
	for key, observation := range live {
		activity.Items[key] = observation
	}
	return activity, nil
}
