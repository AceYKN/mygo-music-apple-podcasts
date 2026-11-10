package apple

import (
	"context"
	"net/http"
	"testing"
)

// A changed route must be observable after refresh, without account headers.
func TestRegionRefreshAndCredentialFreeProbe(t *testing.T) {
	r := NewRegion()
	calls := 0
	route := "us"
	r.HTTP.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "HEAD" || req.Header.Get("Authorization") != "" || req.Header.Get("media-user-token") != "" || req.Header.Get("Cookie") != "" {
			t.Fatal("region probe sent account credentials or fetched page content")
		}
		res := response(200, "")
		res.Request = req.Clone(req.Context())
		res.Request.URL.Path = "/" + route + "/new"
		return res, nil
	})
	if r.Check(context.Background(), "us", false) != "us" {
		t.Fatal("healthy route missing")
	}
	route = "cn"
	if r.Check(context.Background(), "us", false) != "us" || calls != 1 {
		t.Fatal("route cache did not coalesce probes")
	}
	if r.Check(context.Background(), "us", true) != "cn" || calls != 2 {
		t.Fatal("explicit refresh kept an old route")
	}
	if r.CheckShow(context.Background(), "us", "42") != "cn" {
		t.Fatal("missing show's redirect was not detected")
	}
}

// Public diagnostic redirects cannot leave the official Apple Podcasts host.
func TestRegionRejectsForeignRedirect(t *testing.T) {
	r := NewRegion()
	calls := 0
	r.HTTP.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		res := response(302, "")
		res.Request = req
		res.Header.Set("Location", "https://example.invalid/cn/new")
		return res, nil
	})
	if r.Check(context.Background(), "us", false) != "" || calls != 1 {
		t.Fatal("followed a foreign diagnostic redirect")
	}
}
