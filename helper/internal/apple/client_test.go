package apple

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/browser"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func fixture(transport transportFunc) *Client {
	return &Client{Session: func() (browser.Session, error) {
		return browser.Session{Authorized: true, Developer: "test-developer", User: "test-user", Country: "jp"}, nil
	}, HTTP: &http.Client{Transport: transport}}
}

func TestRejectsCredentialDestinations(t *testing.T) {
	for _, path := range []string{"https://evil.invalid/v1/me/library/podcasts", "http://amp-api.podcasts.apple.com/v1/me/storefront", "https://user:pass@amp-api.podcasts.apple.com/v1/me/storefront", "//evil.invalid/v1/me/storefront", "/outside", "/v1/me/storefront#fragment"} {
		if _, err := ResolvePath(path); err == nil {
			t.Fatalf("accepted untrusted path %q", path)
		}
	}
	if _, err := ResolvePath("/v1/me/library/podcasts?offset=60"); err != nil {
		t.Fatal(err)
	}
}
func TestAccountHeaderAndSafeErrors(t *testing.T) {
	c := fixture(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("media-user-token") != "test-user" || r.Header.Get("Music-User-Token") != "" {
			t.Fatal("wrong account header")
		}
		return response(403, `{"errors":[{"detail":"DO-NOT-EXPOSE"}]}`), nil
	})
	_, err := c.Request(context.Background(), "GET", "/v1/me/storefront", nil)
	var e *Error
	if !errors.As(err, &e) || e.Status != 403 || strings.Contains(err.Error(), "DO-NOT-EXPOSE") {
		t.Fatal("unsafe or misclassified account failure")
	}
}
func TestPaginationRejectsForeignNext(t *testing.T) {
	calls := 0
	c := fixture(func(r *http.Request) (*http.Response, error) {
		calls++
		return response(200, `{"data":[{"id":"1","type":"podcasts"}],"next":"https://evil.invalid/v1/next"}`), nil
	})
	if _, err := c.All(context.Background(), "/v1/me/library/podcasts"); err == nil || calls != 1 {
		t.Fatal("followed untrusted pagination")
	}
}
func TestPaginationDeduplicatesAndRejectsLoops(t *testing.T) {
	c := fixture(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("offset") == "2" {
			return response(200, `{"data":[{"id":"1","type":"podcasts"},{"id":"2","type":"podcasts"}]}`), nil
		}
		return response(200, `{"data":[{"id":"1","type":"podcasts"}],"next":"/v1/me/library/podcasts?offset=2"}`), nil
	})
	r, err := c.All(context.Background(), "/v1/me/library/podcasts")
	if err != nil || len(r["data"].([]any)) != 2 {
		t.Fatal("incorrect merged pages", err)
	}
	c = fixture(func(*http.Request) (*http.Response, error) {
		return response(200, `{"data":[{"id":"1"}],"next":"/v1/me/library/podcasts"}`), nil
	})
	if _, err = c.All(context.Background(), "/v1/me/library/podcasts"); err == nil {
		t.Fatal("accepted repeated cursor")
	}
}
func TestLibraryReadbackAfterUnknownWriteResult(t *testing.T) {
	reads, writes := 0, 0
	c := fixture(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			writes++
			return nil, errors.New("connection lost after request")
		}
		reads++
		if reads == 1 {
			return response(404, `{}`), nil
		}
		return response(200, `{"data":[{"id":"42"}]}`), nil
	})
	r, err := c.SetLibrary(context.Background(), "podcasts", "42", true)
	if err != nil || r["confirmed"] != true || writes != 1 {
		t.Fatal("lost or duplicated uncertain write", err)
	}
}
func TestLibraryNoopDoesNotWrite(t *testing.T) {
	c := fixture(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("wrote a state already confirmed")
		}
		return response(200, `{"data":[{"id":"42"}]}`), nil
	})
	r, err := c.SetLibrary(context.Background(), "podcasts", "42", true)
	if err != nil || r["changed"] != false {
		t.Fatal(err)
	}
}
func TestNoTokensInPublicSession(t *testing.T) {
	data, _ := json.Marshal(browser.Session{Authorized: true, Developer: "PRIVATE-DEVELOPER", User: "PRIVATE-USER", ID: "public-account-fingerprint"})
	if strings.Contains(string(data), "PRIVATE-") {
		t.Fatal("serialized credentials")
	}
}
