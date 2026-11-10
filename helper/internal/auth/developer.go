package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var jwtPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
var scriptPattern = regexp.MustCompile(`(?:src|href)=["']([^"']+\.js(?:\?[^"']*)?)["']`)

func tokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

type PublicDeveloper struct {
	mu         sync.Mutex
	HTTP       *http.Client
	cached     string
	retryAfter time.Time
}

func NewPublicDeveloper() *PublicDeveloper {
	return &PublicDeveloper{HTTP: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || r.URL.Host != "podcasts.apple.com" || len(via) > 5 {
			return errors.New("unexpected public token redirect")
		}
		return nil
	}}}
}
func (p *PublicDeveloper) Get(current string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for _, token := range []string{current, p.cached} {
		if tokenExpiry(token).After(now.Add(5 * time.Minute)) {
			return token, nil
		}
	}
	if now.Before(p.retryAfter) {
		if current != "" {
			return current, nil
		}
		return "", errors.New("Apple Podcasts is temporarily unavailable; your saved login was kept")
	}
	p.retryAfter = now.Add(time.Minute)
	queue := []string{"https://podcasts.apple.com/"}
	seen := map[string]bool{}
	for len(queue) > 0 && len(seen) < 12 {
		target := queue[0]
		queue = queue[1:]
		if seen[target] {
			continue
		}
		seen[target] = true
		r, err := p.HTTP.Get(target)
		if err != nil {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		r.Body.Close()
		if readErr != nil || r.StatusCode != 200 {
			continue
		}
		for _, token := range jwtPattern.FindAllString(string(data), -1) {
			if tokenExpiry(token).After(now.Add(5 * time.Minute)) {
				p.cached = token
				p.retryAfter = time.Time{}
				return token, nil
			}
		}
		for _, match := range scriptPattern.FindAllStringSubmatch(string(data), -1) {
			u, err := url.Parse(match[1])
			if err != nil {
				continue
			}
			u = r.Request.URL.ResolveReference(u)
			if u.Scheme == "https" && u.Host == "podcasts.apple.com" && !seen[u.String()] {
				queue = append(queue, u.String())
			}
		}
	}
	if current != "" {
		return current, nil
	}
	return "", errors.New("cannot load the public Apple Podcasts configuration; your saved login was kept")
}
