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
