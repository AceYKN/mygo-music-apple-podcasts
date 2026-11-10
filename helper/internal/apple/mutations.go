package apple

import (
	"context"
	"errors"
	"time"
)

func (c *Client) member(ctx context.Context, kind, id string) (bool, error) {
	r, err := c.Request(ctx, "GET", "/v1/me/library/"+kind+"/"+id+"?platform=web", nil)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Status == 404 {
			return false, nil
		}
		return false, err
	}
	data, _ := r["data"].([]any)
	for _, v := range data {
		m, _ := v.(map[string]any)
		if m["id"] == id {
			return true, nil
		}
	}
	return false, nil
}

// SetLibrary uses the POST/DELETE routes observed in the official Apple web
// application. Re-read even after a network error: a write may have succeeded.
func (c *Client) SetLibrary(ctx context.Context, kind, id string, on bool) (map[string]any, error) {
	if (kind != "podcasts" && kind != "podcast-episodes") || !ValidID.MatchString(id) {
		return nil, &Error{Code: "unavailable", Message: "Invalid library item"}
	}
	before, err := c.member(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if before == on {
		return map[string]any{"confirmed": true, "changed": false}, nil
	}
	method := "DELETE"
	if on {
		method = "POST"
	}
	_, writeErr := c.Request(ctx, method, "/v1/me/library/"+kind+"/"+id+"?platform=web", nil)
	for attempt := 0; attempt < 4; attempt++ {
		state, e := c.member(ctx, kind, id)
		if e == nil && state == on {
			return map[string]any{"confirmed": true, "changed": true}, nil
		}
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(700 * time.Millisecond):
			}
		}
	}
	if writeErr != nil {
		return nil, writeErr
	}
	return nil, &Error{Code: "conflict", Message: "Apple Podcasts has not confirmed the change. Refresh before trying again."}
}

func (c *Client) SetCompleted(ctx context.Context, id string, on bool) (map[string]any, error) {
	if !ValidID.MatchString(id) {
		return nil, &Error{Code: "unavailable", Message: "Invalid episode"}
	}
	body := map[string]any{"type": "playback-positions", "attributes": map[string]any{"completed": on, "recordedAtTimestamp": time.Now().UTC().Format(time.RFC3339Nano)}}
	_, writeErr := c.Request(ctx, "PUT", "/v1/me/playback/positions/podcast-episodes/"+id, body)
	s, err := c.Session()
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 4; attempt++ {
		r, e := c.Request(ctx, "GET", "/v1/catalog/"+s.Country+"/podcast-episodes/"+id+"?include=playback-position&platform=web", nil)
		if e == nil {
			if data, ok := r["data"].([]any); ok && len(data) > 0 {
				item, _ := data[0].(map[string]any)
				rel, _ := item["relationships"].(map[string]any)
				position, _ := rel["playback-position"].(map[string]any)
				states, _ := position["data"].([]any)
				if len(states) > 0 {
					state, _ := states[0].(map[string]any)
					a, _ := state["attributes"].(map[string]any)
					if completed, ok := a["completed"].(bool); ok && completed == on {
						return map[string]any{"confirmed": true}, nil
					}
				}
			}
		}
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(700 * time.Millisecond):
			}
		}
	}
	if writeErr != nil {
		return nil, writeErr
	}
	return nil, &Error{Code: "conflict", Message: "Apple Podcasts has not confirmed the episode completion state"}
}
