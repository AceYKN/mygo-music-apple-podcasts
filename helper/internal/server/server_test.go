package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/apple"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/browser"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/syncstate"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoopbackRPCAuthentication(t *testing.T) {
	s := &Server{Token: strings.Repeat("a", 64)}
	for _, tc := range []struct {
		name, token, origin, body string
		status                    int
	}{
		{"missing secret", "", "", `{"method":"health"}`, 401},
		{"cross origin", s.Token, "https://evil.invalid", `{"method":"health"}`, 403},
		{"invalid json", s.Token, "", `{`, 400},
		{"unknown field", s.Token, "", `{"method":"health","eval":"bad"}`, 400},
		{"valid request", s.Token, "", `{"method":"health"}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://127.0.0.1/rpc", strings.NewReader(tc.body))
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d", w.Code)
			}
			if tc.status == 200 {
				var body map[string]any
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["ok"] != true {
					t.Fatal("wrong health response")
				}
			}
		})
	}
}

func TestProgressIsDurableBeforeCloudWriteAndLatestBackwardIntentWins(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	reads := 0
	position := int64(0)
	at := time.Unix(1800000000, 0).UTC()
	s := &Server{Store: syncstate.New(t.TempDir())}
	s.Apple = testClient(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PUT" {
			var body struct {
				Attributes struct {
					Position int64 `json:"positionInMilliseconds"`
				} `json:"attributes"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("bad position body")
			}
			position = body.Attributes.Position
			return response(204, ""), nil
		}
		reads++
		if reads == 1 {
			close(entered)
			<-release
		}
		data, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"id": "42", "relationships": map[string]any{"playback-position": map[string]any{"data": []any{map[string]any{"attributes": map[string]any{"positionInMilliseconds": position, "completed": false, "recordedAtTimestamp": at.Add(-time.Minute).Format(time.RFC3339Nano)}}}}}}}})
		return response(200, string(data)), nil
	})
	account := "aaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := s.recordProgress(params{ID: "42", Context: account, Position: 120000, Duration: 600000, RecordedAt: at.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := s.recordProgress(params{ID: "42", Context: account, Position: 30000, Duration: 600000, RecordedAt: at.Add(time.Second).Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Store.Read(account)
	if len(state.Pending) != 1 || state.Pending[0].Position != 30000 {
		t.Fatal("latest position was not persisted while network blocked")
	}
	close(release)
	if !s.FlushProgress(5 * time.Second) {
		t.Fatal("progress worker did not finish")
	}
	state, _ = s.Store.Read(account)
	if len(state.Pending) != 0 || state.Progress["42"].Position != 30000 {
		t.Fatal("older response replaced newer backward intent")
	}
}

func TestProgressFromAnotherAccountCannotWrite(t *testing.T) {
	writes := 0
	s := &Server{Store: syncstate.New(t.TempDir()), Apple: testClient(func(*http.Request) (*http.Response, error) { writes++; return response(200, `{}`), nil })}
	if _, err := s.recordProgress(params{ID: "42", Context: "other-account", Position: 30000, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err == nil || writes != 0 {
		t.Fatal("accepted another account's playback event")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}
func testClient(transport transportFunc) *apple.Client {
	return &apple.Client{Session: func() (browser.Session, error) {
		return browser.Session{ID: "aaaaaaaaaaaaaaaaaaaaaaaa", Authorized: true, Country: "us", Developer: "test-developer", User: "test-user"}, nil
	}, HTTP: &http.Client{Transport: transport}}
}
func TestOfflineFallbackDoesNotHideDeniedAccess(t *testing.T) {
	mode := "online"
	s := &Server{Store: syncstate.New(t.TempDir())}
	s.Apple = testClient(func(r *http.Request) (*http.Response, error) {
		switch mode {
		case "offline":
			return nil, errors.New("network down")
		case "denied":
			return response(403, `{}`), nil
		default:
			return response(200, `{"data":[{"id":"42","attributes":{"name":"Example"}}]}`), nil
		}
	})
	if err := s.Store.Verify("aaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	p := params{Path: "/v1/me/library/podcasts", All: true}
	if _, err := s.get(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	mode = "offline"
	result, err := s.get(context.Background(), p)
	if err != nil || result["sync"] == nil {
		t.Fatalf("last confirmed snapshot missing: %v", err)
	}
	mode = "denied"
	if _, err = s.get(context.Background(), p); err == nil {
		t.Fatal("access denial hidden by old cache")
	}
}
func TestFailedWritePersistsAndExplicitRetryReadsBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Store: syncstate.New(dir)}
	s.Apple = testClient(func(*http.Request) (*http.Response, error) { return nil, errors.New("network down") })
	if _, err := s.write(context.Background(), "podcasts", "42", true); err == nil {
		t.Fatal("unconfirmed write reported success")
	}
	state, err := syncstate.New(dir).Read("aaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || len(state.Pending) != 1 {
		t.Fatal("unconfirmed intent not persisted")
	}
	writes := 0
	s.Apple = testClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			writes++
		}
		return response(200, `{"data":[{"id":"42"}]}`), nil
	})
	if _, err = s.dispatch(context.Background(), "sync.retry", params{Operation: state.Pending[0].ID}); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Store.Read("aaaaaaaaaaaaaaaaaaaaaaaa")
	if len(state.Pending) != 0 || writes != 0 {
		t.Fatal("retry duplicated an already applied remote change")
	}
}
func TestAccountChangeCannotCacheAnotherAccountsResponse(t *testing.T) {
	s := &Server{Store: syncstate.New(t.TempDir())}
	changed := false
	s.Apple = testClient(func(*http.Request) (*http.Response, error) {
		changed = true
		return response(200, `{"data":[{"id":"42"}]}`), nil
	})
	base := s.Apple.Session
	s.Apple.Session = func() (browser.Session, error) {
		a, err := base()
		if changed {
			a.ID = "bbbbbbbbbbbbbbbbbbbbbbbb"
		}
		return a, err
	}
	if _, err := s.get(context.Background(), params{Path: "/v1/me/library/podcasts"}); err == nil {
		t.Fatal("account change went unnoticed")
	}
	state, _ := s.Store.Read("bbbbbbbbbbbbbbbbbbbbbbbb")
	if len(state.Snapshots) != 0 {
		t.Fatal("old account data cached for new account")
	}
}

// A region-hidden show must not replace a healthy snapshot with an apparent unfollow.
func TestRegionFilteringPreservesSnapshotAndRecoveryAcceptsRealUnfollow(t *testing.T) {
	filtered, redirected := false, false
	s := &Server{Store: syncstate.New(t.TempDir()), Region: apple.NewRegion()}
	s.Apple = testClient(func(*http.Request) (*http.Response, error) {
		if filtered {
			return response(200, `{"data":[]}`), nil
		}
		return response(200, `{"data":[{"id":"42","attributes":{"name":"Example"}}]}`), nil
	})
	s.Region.HTTP.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		res := response(200, "")
		res.Request = req.Clone(req.Context())
		if redirected && strings.Contains(req.URL.Path, "/podcast/") {
			res.Request.URL.Path = "/cn/new"
		}
		return res, nil
	})
	p := params{Path: "/v1/me/library/podcasts", All: true}
	if _, err := s.get(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	filtered, redirected = true, true
	result, err := s.get(context.Background(), p)
	if err != nil || len(result["data"].([]any)) != 1 || result["sync"].(map[string]any)["reason"] != "region" {
		t.Fatal("regional filtering erased the followed show", err)
	}
	state, _ := s.Store.Read("aaaaaaaaaaaaaaaaaaaaaaaa")
	if len(state.Snapshots[apple.Origin+p.Path+"|all"].Data["data"].([]any)) != 1 {
		t.Fatal("overwrote healthy snapshot")
	}
	redirected = false
	result, err = s.get(context.Background(), p)
	if err != nil || len(result["data"].([]any)) != 0 || result["sync"] != nil {
		t.Fatal("healthy response could not confirm a real unfollow", err)
	}
}

// Do not interpret region-hidden membership as proof an account change succeeded.
func TestRegionMismatchKeepsWritePendingWithoutAccountRequest(t *testing.T) {
	calls := 0
	s := &Server{Store: syncstate.New(t.TempDir()), Region: apple.NewRegion()}
	s.Apple = testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(404, `{}`), nil
	})
	s.Region.HTTP.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		res := response(200, "")
		res.Request = req.Clone(req.Context())
		res.Request.URL.Path = "/cn/new"
		return res, nil
	})
	_, err := s.write(context.Background(), "podcasts", "42", false)
	state, _ := s.Store.Read("aaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil || calls != 0 || len(state.Pending) != 1 || state.Pending[0].Error != "network" {
		t.Fatal("region-hidden membership was treated as a confirmed unfollow")
	}
}
