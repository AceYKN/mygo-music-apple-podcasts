// Package apple adapts the same account endpoints used by the official web player.
package apple

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/browser"
)

const Origin = "https://amp-api.podcasts.apple.com"

var ValidID = regexp.MustCompile(`^[0-9]{1,24}$`)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
	Pending bool   `json:"pending,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type Client struct {
	Browser    *browser.Browser
	Session    func() (browser.Session, error)
	HTTP       *http.Client
	OnResponse func(browser.Session, http.Header) error
	Clock      *RemoteClock
}

// RemoteClock accounts for the API server's clock when comparing listening
// timestamps. Transport round trips and second-resolution Date headers add a
// small uncertainty; they must not turn our own acknowledged seek into conflict.
type RemoteClock struct{ offset atomic.Int64 }

func (c *RemoteClock) observe(header string, sent, received time.Time) {
	date, err := http.ParseTime(header)
	if err != nil {
		return
	}
	delta := date.Sub(sent.Add(received.Sub(sent) / 2))
	if delta > -5*time.Minute && delta < 5*time.Minute {
		c.offset.Store(int64(delta))
	}
}
func (c *RemoteClock) Correct(at time.Time) time.Time {
	if c == nil {
		return at
	}
	return at.Add(time.Duration(c.offset.Load()))
}
func (c *RemoteClock) OffsetMilliseconds() int64 {
	if c == nil {
		return 0
	}
	return time.Duration(c.offset.Load()).Milliseconds()
}

func New(b *browser.Browser) *Client {
	return &Client{Browser: b, Session: b.Session, HTTP: &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func ResolvePath(path string) (string, error) {
	u, err := url.Parse(path)
	if err != nil {
		return "", &Error{Code: "unavailable", Message: "Invalid Apple API address"}
	}
	if u.IsAbs() {
		if u.Scheme != "https" || u.Host != "amp-api.podcasts.apple.com" || u.User != nil {
			return "", &Error{Code: "unavailable", Message: "Rejected an API pagination address outside Apple Podcasts"}
		}
	} else {
		if !strings.HasPrefix(path, "/v1/") {
			return "", &Error{Code: "unavailable", Message: "Invalid Apple API path"}
		}
		u.Scheme = "https"
		u.Host = "amp-api.podcasts.apple.com"
	}
	if !strings.HasPrefix(u.Path, "/v1/") || u.Fragment != "" {
		return "", &Error{Code: "unavailable", Message: "Invalid Apple API path"}
	}
	return u.String(), nil
}

func (c *Client) Request(ctx context.Context, method, path string, body any) (map[string]any, error) {
	target, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}
	s, err := c.Session()
	if err != nil {
		var known *Error
		if errors.As(err, &known) {
			return nil, known
		}
		return nil, &Error{Code: "setup", Message: err.Error()}
	}
	if s.Developer == "" {
		return nil, &Error{Code: "setup", Message: "Apple Podcasts is still loading. Please try again."}
	}
	if strings.Contains(target, "/v1/me/") && !s.Authorized {
		return nil, &Error{Code: "signIn", Message: "Sign in to Apple Podcasts first"}
	}
	var input io.Reader
	if body != nil {
		data, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, input)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Developer)
	req.Header.Set("Origin", "https://podcasts.apple.com")
	req.Header.Set("Referer", "https://podcasts.apple.com/")
	if s.User != "" {
		req.Header.Set("media-user-token", s.User)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	sent := time.Now()
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &Error{Code: "network", Message: "Cannot reach Apple Podcasts. Check your network and retry."}
	}
	defer res.Body.Close()
	if c.Clock != nil {
		c.Clock.observe(res.Header.Get("Date"), sent, time.Now())
	}
	if c.OnResponse != nil {
		if err = c.OnResponse(s, res.Header); err != nil {
			return nil, &Error{Code: "setup", Message: "Cannot securely save the renewed Apple Podcasts login; the previous login was kept"}
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		code := "network"
		message := fmt.Sprintf("Apple Podcasts returned HTTP %d", res.StatusCode)
		switch res.StatusCode {
		case 401:
			code = "signIn"
			message = "Your Apple Podcasts session expired. Sign in again."
		case 403:
			code = "unavailable"
			message = "Apple Podcasts did not authorize this request"
		case 404:
			code = "unavailable"
			message = "This item is not available for this account or region"
		case 429:
			message = "Apple Podcasts is limiting requests. Please wait and retry."
		}
		return nil, &Error{Code: code, Message: message, Status: res.StatusCode}
	}
	if res.StatusCode == 204 {
		return map[string]any{}, nil
	}
	var result map[string]any
	if err = json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&result); err != nil {
		if err == io.EOF {
			return map[string]any{}, nil
		}
		return nil, &Error{Code: "unavailable", Message: "Apple Podcasts returned an unreadable response"}
	}
	return result, nil
}

func (c *Client) All(ctx context.Context, path string) (map[string]any, error) {
	items := []any{}
	seen := map[string]bool{}
	itemIDs := map[string]bool{}
	for page := 0; path != "" && page < 200; page++ {
		canonical, err := ResolvePath(path)
		if err != nil {
			return nil, err
		}
		if seen[canonical] {
			return nil, &Error{Code: "network", Message: "Apple Podcasts repeated a pagination cursor"}
		}
		seen[canonical] = true
		r, err := c.Request(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		data, _ := r["data"].([]any)
		for _, item := range data {
			m, _ := item.(map[string]any)
			id, _ := m["id"].(string)
			key := fmt.Sprint(m["type"]) + ":" + id
			if id == "" || !itemIDs[key] {
				items = append(items, item)
				itemIDs[key] = true
			}
		}
		path, _ = r["next"].(string)
		if page == 199 && path != "" {
			return nil, &Error{Code: "network", Message: "Apple Podcasts library exceeds the pagination limit"}
		}
	}
	return map[string]any{"data": items}, nil
}
