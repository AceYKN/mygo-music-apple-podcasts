package apple

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func positionResponse(position int64, completed bool, at string) map[string]any {
	return map[string]any{"data": []any{map[string]any{"id": "42", "relationships": map[string]any{"playback-position": map[string]any{"data": []any{map[string]any{"attributes": map[string]any{"positionInMilliseconds": position, "completed": completed, "recordedAtTimestamp": at}}}}}}}}
}
func TestExplicitPositionCanMoveBackwardAndConfirmsReadback(t *testing.T) {
	at := time.Unix(1800000000, 0).UTC()
	position := int64(120000)
	writes := 0
	c := fixture(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PUT" {
			writes++
			var body struct {
				Attributes struct {
					Position int64 `json:"positionInMilliseconds"`
				} `json:"attributes"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid position body")
			}
			position = body.Attributes.Position
			return response(204, ""), nil
		}
		data, _ := json.Marshal(positionResponse(position, false, at.Add(-time.Minute).Format(time.RFC3339Nano)))
		return response(200, string(data)), nil
	})
	if _, err := c.SetPosition(context.Background(), "42", 30000, false, at.Format(time.RFC3339Nano)); err != nil || position != 30000 || writes != 1 {
		t.Fatal("backward seek not confirmed", err)
	}
}
func TestOlderProgressDoesNotOverwriteAnotherDevice(t *testing.T) {
	at := time.Unix(1800000000, 0).UTC()
	writes := 0
	c := fixture(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			writes++
		}
		data, _ := json.Marshal(positionResponse(90000, false, at.Add(time.Minute).Format(time.RFC3339Nano)))
		return response(200, string(data)), nil
	})
	if _, err := c.SetPosition(context.Background(), "42", 30000, false, at.Format(time.RFC3339Nano)); err == nil || writes != 0 {
		t.Fatal("overwrote a newer cloud position")
	}
}

func TestServerClockSkewDoesNotMisclassifyOurOwnProgress(t *testing.T) {
	clientTime := time.Unix(1800000000, 0).UTC()
	clock := &RemoteClock{}
	clock.observe(clientTime.Add(5*time.Second).Format(http.TimeFormat), clientTime, clientTime)
	position := int64(30000)
	c := fixture(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PUT" {
			var body struct {
				Attributes struct {
					Position int64  `json:"positionInMilliseconds"`
					At       string `json:"recordedAtTimestamp"`
				} `json:"attributes"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			position = body.Attributes.Position
			if body.Attributes.At != clientTime.Add(5*time.Second).Format(time.RFC3339Nano) {
				t.Error("timestamp did not use the API clock")
			}
			return response(204, ""), nil
		}
		data, _ := json.Marshal(positionResponse(position, false, clientTime.Add(4*time.Second).Format(time.RFC3339Nano)))
		return response(200, string(data)), nil
	})
	c.Clock = clock
	if _, err := c.SetPosition(context.Background(), "42", 45000, false, clientTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatal("server clock skew caused a false conflict", err)
	}
}
func TestPublisherProbeNeverReceivesAppleCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("media-user-token") != "" || r.Header.Get("Cookie") != "" {
			t.Error("publisher received account credentials")
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(206)
		w.Write([]byte("ID3-test"))
	}))
	defer server.Close()
	format, err := SniffMedia(context.Background(), server.URL+"/audio")
	if err != nil || format != "mp3" {
		t.Fatal("cannot identify audio", err)
	}
	if _, _, err := NativeMedia(map[string]any{"attributes": map[string]any{"assetUrl": "https://example.invalid/paid.m3u8"}}); err == nil {
		t.Fatal("protected manifest was presented as native audio")
	}
}
