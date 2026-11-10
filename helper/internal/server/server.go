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

	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/apple"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/browser"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/syncstate"
)

type Server struct {
	Token   string
	Browser *browser.Browser
	Apple   *apple.Client
	Store   *syncstate.Store
	writeMu sync.Mutex
}
type command struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type params struct {
	Path      string  `json:"path"`
	All       bool    `json:"all"`
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	On        bool    `json:"on"`
	Action    string  `json:"action"`
	Value     float64 `json:"value"`
	Items     []any   `json:"items"`
	Position  int64   `json:"position"`
	Operation string  `json:"operation"`
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
		var playerError *browser.PlaybackError
		if errors.As(err, &playerError) {
			e = &apple.Error{Code: "unavailable", Message: playerError.Message}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": e})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
}

func (s *Server) dispatch(ctx context.Context, method string, p params) (any, error) {
	switch method {
	case "health":
		return map[string]any{"protocol": 1, "version": "0.1.0-preview.1", "playback": "remote"}, nil
	case "auth.status":
		a, err := s.Apple.Session()
		if err != nil {
			return nil, &apple.Error{Code: "setup", Message: err.Error()}
		}
		if a.Authorized {
			var storefront map[string]any
			if storefront, err = s.Apple.Request(ctx, "GET", "/v1/me/storefront", nil); err != nil {
				var e *apple.Error
				if errors.As(err, &e) && (e.Status == 401 || e.Status == 403) {
					a.Authorized = false
				} else {
					if s.Store == nil {
						return nil, err
					}
					state, readErr := s.Store.Read(a.ID)
					if readErr != nil || !state.Verified || !errors.As(err, &e) || e.Code != "network" {
						return nil, err
					}
					a.Offline = true
				}
			} else {
				if data, ok := storefront["data"].([]any); ok && len(data) > 0 {
					if item, ok := data[0].(map[string]any); ok {
						if country, ok := item["id"].(string); ok && len(country) == 2 {
							a.Country = country
						}
					}
				}
				if s.Store != nil {
					if err = s.Store.Verify(a.ID); err != nil {
						return nil, localError()
					}
				}
			}
		}
		return a, nil
	case "auth.start":
		if err := s.Browser.SignIn(); err != nil {
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
		if err = s.Browser.SignOut(); err != nil {
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
				return s.execute(ctx, a.ID, op)
			}
		}
		return nil, &apple.Error{Code: "conflict", Message: "This pending change no longer exists. Refresh the sync page."}
	case "player.state":
		return s.Browser.State()
	case "player.control":
		return true, s.Browser.Control(p.Action, p.Value)
	case "player.play":
		if len(p.Items) == 0 || len(p.Items) > 200 || p.Position < 0 {
			return nil, &apple.Error{Code: "unavailable", Message: "Invalid playback queue"}
		}
		return true, s.Browser.Play(p.Items, p.Position)
	case "diagnostics":
		return s.Browser.Inspect()
	case "window.show":
		return true, s.Browser.ShowWindow()
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
	if op.Kind == "completed" {
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
