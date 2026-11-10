package apple

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Region observes public website routing without sending account credentials.
type Region struct {
	mu             sync.Mutex
	HTTP           *http.Client
	country, route string
	checked        time.Time
}

func NewRegion() *Region {
	return &Region{HTTP: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || r.URL.Host != "podcasts.apple.com" || len(via) > 5 {
			return http.ErrUseLastResponse
		}
		return nil
	}}}
}
func (r *Region) Check(ctx context.Context, country string, force bool) string {
	country = strings.ToLower(country)
	if r == nil || !validCountry(country) {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !force && r.country == country && time.Since(r.checked) < time.Minute {
		return r.route
	}
	r.country = country
	r.checked = time.Now()
	r.route = ""
	r.route = r.probe(ctx, "/"+country+"/new")
	return r.route
}

// CheckShow also checks a missing show's public page: a regional home page
// can stay reachable while a particular foreign show redirects elsewhere.
func (r *Region) CheckShow(ctx context.Context, country, id string) string {
	country = strings.ToLower(country)
	if r == nil || !validCountry(country) || !ValidID.MatchString(id) {
		return ""
	}
	return r.probe(ctx, "/"+country+"/podcast/id"+id)
}

func validCountry(country string) bool {
	return len(country) == 2 && country[0] >= 'a' && country[0] <= 'z' && country[1] >= 'a' && country[1] <= 'z'
}

func (r *Region) probe(ctx context.Context, path string) string {
	if r.HTTP == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "HEAD", "https://podcasts.apple.com"+path, nil)
	if err != nil {
		return ""
	}
	response, err := r.HTTP.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Request == nil {
		return ""
	}
	parts := strings.Split(strings.Trim(response.Request.URL.Path, "/"), "/")
	if len(parts) > 0 && validCountry(strings.ToLower(parts[0])) {
		return strings.ToLower(parts[0])
	}
	return ""
}
