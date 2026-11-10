package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/apple"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/auth"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/browser"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/syncstate"
)

type Server struct {
	Token           string
	Apple           *apple.Client
	Store           *syncstate.Store
	Auth            *auth.Manager
	Region          *apple.Region
	writeMu         sync.Mutex
	progressMu      sync.Mutex
	progressQueue   []progressWork
	progressRunning bool
	progressDone    chan struct{}
}
type command struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type params struct {
	Path       string  `json:"path"`
	All        bool    `json:"all"`
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	On         bool    `json:"on"`
	Action     string  `json:"action"`
	Value      float64 `json:"value"`
	Items      []any   `json:"items"`
	Position   int64   `json:"position"`
	Operation  string  `json:"operation"`
	Context    string  `json:"context"`
	Duration   int64   `json:"duration"`
	Completed  bool    `json:"completed"`
	RecordedAt string  `json:"recordedAt"`
	Reason     string  `json:"reason"`
	Refresh    bool    `json:"refresh"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != "POST" || r.URL.Path != "/rpc" {
		http.Error(w, `{"ok":false}`, 404)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, `{"ok":false}`, 403)
		return
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(s.Token)) != 1 {
		http.Error(w, `{"ok":false}`, 401)
		return
	}
	var call command
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&call); err != nil {
		http.Error(w, `{"ok":false,"error":{"code":"unavailable","message":"Invalid request"}}`, 400)
		return
	}
	var p params
	if len(call.Params) > 0 {
		if err := json.Unmarshal(call.Params, &p); err != nil {
			http.Error(w, `{"ok":false}`, 400)
			return
		}
	}
	data, err := s.dispatch(r.Context(), call.Method, p)
	if err != nil {
		e := &apple.Error{Code: "unavailable", Message: "Apple Podcasts could not complete the operation"}
		var app *apple.Error
		if errors.As(err, &app) {
			e = app
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": e})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
}

func (s *Server) dispatch(ctx context.Context, method string, p params) (any, error) {
	switch method {
	case "health":
		return map[string]any{"protocol": 1, "version": "0.2.0-preview.1", "playback": "stream"}, nil
	case "auth.status":
		if p.Refresh && s.Apple.HTTP != nil {
			s.Apple.HTTP.CloseIdleConnections()
		}
		a, err := s.accountStatus(ctx, true)
		if err == nil && s.Region != nil {
			a.RouteCountry = s.Region.Check(ctx, a.Country, p.Refresh)
		}
		return a, err
	case "auth.start":
		if s.Auth == nil {
			return nil, localError()
		}
		if err := s.Auth.SignIn(); err != nil {
			return nil, &apple.Error{Code: "setup", Message: err.Error()}
		}
		return map[string]any{"started": true}, nil
	case "auth.signout":
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		a, err := s.Apple.Session()
		if err != nil {
			return nil, err
		}
		if s.Auth == nil {
			return nil, localError()
		}
		if err = s.Auth.SignOut(); err != nil {
			return nil, err
		}
		if s.Store != nil {
			if err = s.Store.Clear(a.ID); err != nil {
				return nil, localError()
			}
		}
		return true, nil
	case "api.get":
		return s.get(ctx, p)
	case "library.set":
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		return s.write(ctx, p.Kind, p.ID, p.On)
	case "completion.set":
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		return s.write(ctx, "completed", p.ID, p.On)
	case "sync.status":
		if s.Store == nil {
			return nil, localError()
		}
		a, err := s.Apple.Session()
		if err != nil {
			return nil, err
		}
		if !a.Authorized {
			return nil, &apple.Error{Code: "signIn", Message: "Sign in to Apple Podcasts first"}
		}
		state, err := s.Store.Read(a.ID)
		if err != nil {
			return nil, localError()
		}
		return map[string]any{"pending": state.Pending, "snapshots": len(state.Snapshots)}, nil
	case "sync.retry", "sync.discard":
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		if s.Store == nil {
			return nil, localError()
		}
		a, err := s.Apple.Session()
		if err != nil {
			return nil, err
		}
		if !a.Authorized {
			return nil, &apple.Error{Code: "signIn", Message: "Sign in to Apple Podcasts first"}
		}
		state, err := s.Store.Read(a.ID)
		if err != nil {
			return nil, localError()
		}
		for _, op := range state.Pending {
			if op.ID == p.Operation {
				if method == "sync.discard" {
					return true, s.Store.Resolve(a.ID, op.ID, "")
				}
				if op.Kind == "position" {
					var err error
					op, _, err = s.Store.QueuePosition(a.ID, op.Item, op.Position, op.Duration, op.On, time.Now().UTC().Format(time.RFC3339Nano), true)
					if err != nil {
						return nil, localError()
					}
				}
				return s.execute(ctx, a.ID, op)
			}
		}
		return nil, &apple.Error{Code: "conflict", Message: "This pending change no longer exists. Refresh the sync page."}
	case "media.stream":
		a, err := s.Apple.Session()
		if err != nil {
			return nil, err
		}
		item, err := s.accountClient(a.ID).Episode(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		after, err := s.Apple.Session()
		if err != nil {
			return nil, err
		}
		if after.ID != a.ID {
			return nil, &apple.Error{Code: "conflict", Message: "The Apple Podcasts account changed. Refresh before continuing."}
		}
		asset, format, err := apple.NativeMedia(item)
		if err != nil {
			return nil, err
		}
		if format == "" {
			format, err = apple.SniffMedia(ctx, asset)
			if err != nil {
				return nil, err
			}
		}
		return map[string]any{"url": asset, "type": format}, nil
	case "progress.start":
		a, err := s.Apple.Session()
		if err != nil {
			return nil, err
		}
		if !a.Authorized {
			return map[string]any{"position": 0, "context": ""}, nil
		}
		item, err := s.accountClient(a.ID).Episode(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if _, _, err = apple.NativeMedia(item); err != nil {
			return nil, err
		}
		position := apple.ReadPosition(item)
		if position.Completed {
			position.Position = 0
		}
		return map[string]any{"position": position.Position, "context": a.ID}, nil
	case "progress.record":
		return s.recordProgress(p)
	case "progress.flush":
		return map[string]any{"flushed": s.FlushProgress(7 * time.Second)}, nil
	case "diagnostics":
		if s.Auth == nil {
			return map[string]any{"chromePlayback": false}, nil
		}
		info := s.Auth.Diagnostics()
		info["apiClockOffsetMs"] = s.Apple.Clock.OffsetMilliseconds()
		return info, nil
	default:
		return nil, &apple.Error{Code: "unsupported", Message: "This operation is not implemented"}
	}
}

func localError() *apple.Error {
	return &apple.Error{Code: "setup", Message: "Cannot save local synchronization state. Check available disk space and file permissions."}
}

func (s *Server) get(ctx context.Context, p params) (map[string]any, error) {
	path, err := apple.ResolvePath(p.Path)
	if err != nil {
		return nil, err
	}
	key := path
	if p.All {
		key += "|all"
	}
	a, sessionErr := s.Apple.Session()
	if sessionErr != nil {
		return nil, sessionErr
	}
	client := s.accountClient(a.ID)
	var result map[string]any
	if p.All {
		result, err = client.All(ctx, path)
	} else {
		result, err = client.Request(ctx, "GET", path, nil)
	}
	if s.Store == nil || !a.Authorized {
		return result, err
	}
	after, sessionErr := s.Apple.Session()
	if sessionErr != nil || after.ID != a.ID {
		return nil, &apple.Error{Code: "conflict", Message: "The Apple Podcasts account changed. Refresh before continuing."}
	}
	if err == nil {
		if strings.Contains(path, "/v1/me/library/") {
			state, e := s.Store.Read(a.ID)
			if e != nil {
				return nil, localError()
			}
			route := s.Region.Check(ctx, a.Country, false)
			cached, hasCache := state.Snapshots[key]
			if (route == "" || route == a.Country) && hasCache && strings.SplitN(path, "?", 2)[0] == apple.Origin+"/v1/me/library/podcasts" {
				if id := missingShow(cached.Data, result); id != "" {
					route = s.Region.CheckShow(ctx, a.Country, id)
				}
			}
			if route != "" && route != a.Country {
				if hasCache {
					cached.Data["sync"] = map[string]any{"stale": true, "at": cached.At, "reason": "region", "routeCountry": route}
					return cached.Data, nil
				}
				result["sync"] = map[string]any{"stale": false, "reason": "region", "routeCountry": route}
				return result, nil
			}
		}
		if saveErr := s.Store.Cache(a.ID, key, result); saveErr != nil {
			return nil, localError()
		}
		return result, nil
	}
	var remote *apple.Error
	// An expired login or denied entitlement must never fall back to old data.
	if !errors.As(err, &remote) || remote.Code != "network" {
		return nil, err
	}
	state, readErr := s.Store.Read(a.ID)
	if readErr != nil {
		return nil, localError()
	}
	if cached, ok := state.Snapshots[key]; ok && state.Verified {
		cached.Data["sync"] = map[string]any{"stale": true, "at": cached.At}
		return cached.Data, nil
	}
	return nil, err
}

func missingShow(before, after map[string]any) string {
	old, _ := before["data"].([]any)
	current, _ := after["data"].([]any)
	ids := map[string]bool{}
	for _, raw := range current {
		item, _ := raw.(map[string]any)
		id, _ := item["id"].(string)
		ids[id] = true
	}
	for _, raw := range old {
		item, _ := raw.(map[string]any)
		id, _ := item["id"].(string)
		if apple.ValidID.MatchString(id) && !ids[id] {
			return id
		}
	}
	return ""
}

func (s *Server) write(ctx context.Context, kind, id string, on bool) (map[string]any, error) {
	if !apple.ValidID.MatchString(id) || (kind != "podcasts" && kind != "podcast-episodes" && kind != "completed") {
		return nil, &apple.Error{Code: "unavailable", Message: "Invalid synchronization action"}
	}
	if s.Store == nil {
		if kind == "completed" {
			return s.Apple.SetCompleted(ctx, id, on)
		}
		return s.Apple.SetLibrary(ctx, kind, id, on)
	}
	a, err := s.Apple.Session()
	if err != nil {
		return nil, err
	}
	if !a.Authorized {
		return nil, &apple.Error{Code: "signIn", Message: "Sign in to Apple Podcasts first"}
	}
	op, err := s.Store.Queue(a.ID, kind, id, on)
	if err != nil {
		return nil, localError()
	}
	return s.execute(ctx, a.ID, op)
}
func (s *Server) execute(ctx context.Context, account string, op syncstate.Operation) (map[string]any, error) {
	var result map[string]any
	var err error
	client := s.accountClient(account)
	a, sessionErr := client.Session()
	if sessionErr != nil {
		err = sessionErr
	} else {
		route := s.Region.Check(ctx, a.Country, false)
		if (route == "" || route == a.Country) && op.Kind == "podcasts" {
			route = s.Region.CheckShow(ctx, a.Country, op.Item)
		}
		if route != "" && route != a.Country {
			err = &apple.Error{Code: "network", Message: "Apple Podcasts is routing this network to another region. Check your proxy rules before confirming account changes."}
		}
	}
	if err != nil {
		// Preserve the pending intent instead of treating a region-hidden 404
		// as proof that an episode or followed show was removed.
	} else if op.Kind == "position" {
		result, err = client.SetPosition(ctx, op.Item, op.Position, op.On, op.RecordedAt)
	} else if op.Kind == "completed" {
		result, err = client.SetCompleted(ctx, op.Item, op.On)
	} else {
		result, err = client.SetLibrary(ctx, op.Kind, op.Item, op.On)
	}
	if err == nil {
		if s.Store.Resolve(account, op.ID, "") != nil {
			return nil, localError()
		}
		return result, nil
	}
	code := "network"
	message := "Apple Podcasts connection failed. Check the official window and retry."
	var remote *apple.Error
	if errors.As(err, &remote) {
		code = remote.Code
		message = remote.Message
	}
	if s.Store.Resolve(account, op.ID, code) != nil {
		return nil, localError()
	}
	return nil, &apple.Error{Code: code, Message: message, Pending: true}
}

func (s *Server) accountStatus(ctx context.Context, repair bool) (browser.Session, error) {
	a, err := s.Apple.Session()
	if err != nil {
		return a, &apple.Error{Code: "setup", Message: err.Error()}
	}
	if !a.Authorized {
		return a, nil
	}
	r, err := s.accountClient(a.ID).Request(ctx, "GET", "/v1/me/storefront", nil)
	if err != nil {
		var e *apple.Error
		if errors.As(err, &e) && (e.Status == 401 || e.Status == 403) {
			if repair && s.Auth != nil {
				changed, _ := s.Auth.Repair()
				if changed {
					recovered, err := s.accountStatus(ctx, false)
					if err != nil || !recovered.Authorized {
						s.Auth.CancelRepair()
					}
					return recovered, err
				}
			}
			a.Authorized = false
			return a, nil
		}
		if s.Store != nil {
			state, readErr := s.Store.Read(a.ID)
			if readErr == nil && state.Verified && errors.As(err, &e) && e.Code == "network" {
				a.Offline = true
				return a, nil
			}
		}
		return a, err
	}
	if data, ok := r["data"].([]any); ok && len(data) > 0 {
		if item, ok := data[0].(map[string]any); ok {
			if country, ok := item["id"].(string); ok && len(country) == 2 {
				a.Country = country
			}
		}
	}
	if s.Auth != nil {
		fresh, refreshErr := s.Auth.Session()
		if refreshErr != nil {
			return a, refreshErr
		}
		if fresh.ID != a.ID {
			return a, &apple.Error{Code: "conflict", Message: "The Apple Podcasts account changed. Refresh before continuing."}
		}
		a.User = fresh.User
		a.Developer = fresh.Developer
		if err = s.Auth.Confirm(a); err != nil {
			return a, &apple.Error{Code: "setup", Message: err.Error()}
		}
	}
	if s.Store != nil {
		if s.Store.Verify(a.ID) != nil {
			return a, localError()
		}
	}
	return a, nil
}

type progressWork struct {
	account string
	op      syncstate.Operation
}

func (s *Server) recordProgress(p params) (map[string]any, error) {
	if s.Store == nil || !apple.ValidID.MatchString(p.ID) || p.Position < 0 || p.Duration < 0 {
		return nil, &apple.Error{Code: "unavailable", Message: "Invalid playback progress"}
	}
	if _, err := time.Parse(time.RFC3339Nano, p.RecordedAt); err != nil {
		return nil, &apple.Error{Code: "unavailable", Message: "Invalid playback timestamp"}
	}
	a, err := s.Apple.Session()
	if err != nil {
		return nil, err
	}
	if !a.Authorized || p.Context != a.ID {
		return nil, &apple.Error{Code: "conflict", Message: "The playback belongs to a different account session. Start the episode again."}
	}
	op, accepted, err := s.Store.QueuePosition(a.ID, p.ID, p.Position, p.Duration, p.Completed, p.RecordedAt, false)
	if err != nil {
		return nil, localError()
	}
	if accepted {
		s.enqueueProgress(progressWork{account: a.ID, op: op})
	}
	return map[string]any{"queued": accepted}, nil
}
func (s *Server) enqueueProgress(work progressWork) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	replaced := false
	for i, old := range s.progressQueue {
		if old.account == work.account && old.op.Item == work.op.Item {
			s.progressQueue[i] = work
			replaced = true
			break
		}
	}
	if !replaced {
		s.progressQueue = append(s.progressQueue, work)
	}
	if s.progressRunning {
		return
	}
	s.progressRunning = true
	s.progressDone = make(chan struct{})
	go func() {
		for {
			s.progressMu.Lock()
			if len(s.progressQueue) == 0 {
				s.progressRunning = false
				close(s.progressDone)
				s.progressMu.Unlock()
				return
			}
			next := s.progressQueue[0]
			s.progressQueue = s.progressQueue[1:]
			s.progressMu.Unlock()
			s.writeMu.Lock()
			_, _ = s.execute(context.Background(), next.account, next.op)
			s.writeMu.Unlock()
		}
	}()
}
func (s *Server) FlushProgress(timeout time.Duration) bool {
	s.progressMu.Lock()
	done, running := s.progressDone, s.progressRunning
	s.progressMu.Unlock()
	if !running {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
func (s *Server) accountClient(account string) *apple.Client {
	client := *s.Apple
	client.Session = func() (browser.Session, error) {
		a, err := s.Apple.Session()
		if err != nil {
			return a, err
		}
		if a.ID != account {
			return browser.Session{}, &apple.Error{Code: "conflict", Message: "The Apple Podcasts account changed. Refresh before continuing."}
		}
		return a, nil
	}
	return &client
}
