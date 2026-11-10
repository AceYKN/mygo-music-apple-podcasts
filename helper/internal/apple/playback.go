package apple

import (
	"context"
	"math"
	"time"
)

type Position struct {
	Position   int64
	Completed  bool
	RecordedAt string
}

func (c *Client) Episode(ctx context.Context, id string) (map[string]any, error) {
	if !ValidID.MatchString(id) {
		return nil, &Error{Code: "unavailable", Message: "Invalid episode"}
	}
	s, err := c.Session()
	if err != nil {
		return nil, err
	}
	r, err := c.Request(ctx, "GET", "/v1/catalog/"+s.Country+"/podcast-episodes/"+id+"?include=podcast,playback-position&platform=web&with=entitlements", nil)
	if err != nil {
		return nil, err
	}
	data, _ := r["data"].([]any)
	if len(data) == 0 {
		return nil, &Error{Code: "unavailable", Message: "This episode is not available"}
	}
	item, _ := data[0].(map[string]any)
	return item, nil
}
func ReadPosition(item map[string]any) Position {
	rel, _ := item["relationships"].(map[string]any)
	position, _ := rel["playback-position"].(map[string]any)
	data, _ := position["data"].([]any)
	if len(data) == 0 {
		return Position{}
	}
	first, _ := data[0].(map[string]any)
	a, _ := first["attributes"].(map[string]any)
	value, _ := a["positionInMilliseconds"].(float64)
	completed, _ := a["completed"].(bool)
	at, _ := a["recordedAtTimestamp"].(string)
	return Position{Position: int64(value), Completed: completed, RecordedAt: at}
}
func (c *Client) SetPosition(ctx context.Context, id string, position int64, completed bool, recordedAt string) (map[string]any, error) {
	if !ValidID.MatchString(id) || position < 0 {
		return nil, &Error{Code: "unavailable", Message: "Invalid playback position"}
	}
	when, err := time.Parse(time.RFC3339Nano, recordedAt)
	if err != nil {
		return nil, &Error{Code: "unavailable", Message: "Invalid playback timestamp"}
	}
	before, err := c.Episode(ctx, id)
	if err != nil {
		return nil, err
	}
	cloud := ReadPosition(before)
	when = c.Clock.Correct(when)
	if at, e := time.Parse(time.RFC3339Nano, cloud.RecordedAt); e == nil && at.After(when.Add(2*time.Second)) && math.Abs(float64(cloud.Position-position)) > 2000 {
		return nil, &Error{Code: "conflict", Message: "Another device has newer listening progress. Choose whether to retry this position or keep the cloud position."}
	}
	if cloud.Completed == completed && math.Abs(float64(cloud.Position-position)) <= 1000 {
		return map[string]any{"confirmed": true, "changed": false}, nil
	}
	body := map[string]any{"type": "playback-positions", "attributes": map[string]any{"completed": completed, "positionInMilliseconds": position, "recordedAtTimestamp": when.UTC().Format(time.RFC3339Nano)}}
	_, writeErr := c.Request(ctx, "PUT", "/v1/me/playback/positions/podcast-episodes/"+id, body)
	for attempt := 0; attempt < 4; attempt++ {
		item, e := c.Episode(ctx, id)
		if e == nil {
			state := ReadPosition(item)
			if state.Completed == completed && math.Abs(float64(state.Position-position)) <= 2000 {
				return map[string]any{"confirmed": true}, nil
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
	return nil, &Error{Code: "conflict", Message: "Apple Podcasts has not confirmed the playback position. Check Playback and Sync."}
}
