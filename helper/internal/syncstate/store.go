// Package syncstate keeps display snapshots and unconfirmed user actions.
// It never replays account writes in the background.
package syncstate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

type Operation struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Item       string `json:"item"`
	On         bool   `json:"on"`
	Created    string `json:"created"`
	Error      string `json:"error,omitempty"`
	Position   int64  `json:"position,omitempty"`
	Duration   int64  `json:"duration,omitempty"`
	RecordedAt string `json:"recordedAt,omitempty"`
}
type Snapshot struct {
	At   string         `json:"at"`
	Data map[string]any `json:"data"`
}
type State struct {
	Verified  bool                 `json:"verified"`
	Snapshots map[string]Snapshot  `json:"snapshots"`
	Pending   []Operation          `json:"pending"`
	Progress  map[string]Operation `json:"progress,omitempty"`
}
type Store struct {
	mu  sync.Mutex
	dir string
}

var validAccount = regexp.MustCompile(`^[a-f0-9]{24}$`)

func New(dir string) *Store { return &Store{dir: dir} }
func (s *Store) path(account string) (string, error) {
	if !validAccount.MatchString(account) {
		return "", errors.New("invalid account namespace")
	}
	return filepath.Join(s.dir, account+".json"), nil
}
func (s *Store) load(account string) (State, error) {
	state := State{Snapshots: map[string]Snapshot{}, Pending: []Operation{}}
	path, err := s.path(account)
	if err != nil {
		return state, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, errors.New("cannot read local synchronization state")
	}
	if json.Unmarshal(data, &state) != nil {
		return state, errors.New("local synchronization state is unreadable")
	}
	if state.Snapshots == nil {
		state.Snapshots = map[string]Snapshot{}
	}
	if state.Pending == nil {
		state.Pending = []Operation{}
	}
	if state.Progress == nil {
		state.Progress = map[string]Operation{}
	}
	return state, nil
}
func (s *Store) save(account string, state State) error {
	path, err := s.path(account)
	if err != nil {
		return err
	}
	if os.MkdirAll(s.dir, 0700) != nil {
		return errors.New("cannot create local synchronization directory")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, "sync-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}
func (s *Store) Read(account string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(account)
}
func (s *Store) Verify(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load(account)
	if err != nil {
		return err
	}
	if state.Verified {
		return nil
	}
	state.Verified = true
	return s.save(account, state)
}
func (s *Store) Cache(account, path string, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load(account)
	if err != nil {
		return err
	}
	state.Snapshots[path] = Snapshot{At: time.Now().UTC().Format(time.RFC3339), Data: display(data)}
	// Bound cache growth independently from the pending-operation journal.
	if len(state.Snapshots) > 100 {
		oldest := ""
		for key, v := range state.Snapshots {
			if key != path && (oldest == "" || v.At < state.Snapshots[oldest].At) {
				oldest = key
			}
		}
		delete(state.Snapshots, oldest)
	}
	return s.save(account, state)
}
func (s *Store) Queue(account, kind, item string, on bool) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load(account)
	if err != nil {
		return Operation{}, err
	}
	bytes := make([]byte, 12)
	if _, err = rand.Read(bytes); err != nil {
		return Operation{}, err
	}
	op := Operation{ID: hex.EncodeToString(bytes), Kind: kind, Item: item, On: on, Created: time.Now().UTC().Format(time.RFC3339)}
	for i, old := range state.Pending {
		if old.Kind == kind && old.Item == item {
			state.Pending[i] = op
			return op, s.save(account, state)
		}
	}
	state.Pending = append(state.Pending, op)
	return op, s.save(account, state)
}
func (s *Store) Resolve(account, id, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load(account)
	if err != nil {
		return err
	}
	for i, op := range state.Pending {
		if op.ID == id {
			if message == "" {
				if op.Kind == "position" {
					state.Progress[op.Item] = op
				}
				state.Pending = append(state.Pending[:i], state.Pending[i+1:]...)
			} else {
				state.Pending[i].Error = message
			}
			return s.save(account, state)
		}
	}
	return nil
}

// QueuePosition rejects stale callbacks and holds conflicts until the listener
// explicitly retries or discards them. Fresh intent replaces an offline tick.
func (s *Store) QueuePosition(account, item string, position, duration int64, completed bool, recordedAt string, force bool) (Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load(account)
	if err != nil {
		return Operation{}, false, err
	}
	when, err := time.Parse(time.RFC3339Nano, recordedAt)
	if err != nil {
		return Operation{}, false, err
	}
	if last, ok := state.Progress[item]; ok && !force {
		if previous, e := time.Parse(time.RFC3339Nano, last.RecordedAt); e == nil && !when.After(previous) {
			return last, false, nil
		}
	}
	index := -1
	for i, old := range state.Pending {
		if old.Kind == "position" && old.Item == item {
			if !force {
				previous, e := time.Parse(time.RFC3339Nano, old.RecordedAt)
				if old.Error == "conflict" || (e == nil && !when.After(previous)) {
					return old, false, nil
				}
			}
			index = i
			break
		}
	}
	bytes := make([]byte, 12)
	if _, err = rand.Read(bytes); err != nil {
		return Operation{}, false, err
	}
	op := Operation{ID: hex.EncodeToString(bytes), Kind: "position", Item: item, On: completed, Created: time.Now().UTC().Format(time.RFC3339), Position: position, Duration: duration, RecordedAt: recordedAt}
	if index < 0 {
		state.Pending = append(state.Pending, op)
	} else {
		state.Pending[index] = op
	}
	return op, true, s.save(account, state)
}
func (s *Store) Clear(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(account)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Only display metadata is cached. Playback assets, offers, tokens and arbitrary
// provider attributes cannot enter the snapshot through this whitelist.
func display(input map[string]any) map[string]any {
	keys := map[string]bool{"id": true, "type": true, "data": true, "results": true, "podcasts": true, "podcast-episodes": true, "podcast-channels": true, "next": true, "attributes": true, "relationships": true, "podcast": true, "episodes": true, "playback-position": true,
		"name": true, "artistName": true, "description": true, "standard": true, "short": true, "artwork": true, "url": true, "width": true, "height": true, "trackCount": true, "inLibrary": true, "podcastId": true, "durationInMilliseconds": true, "durationInMillis": true, "releaseDateTime": true, "releaseDate": true, "isPlayable": true, "completed": true, "positionInMilliseconds": true, "recordedAtTimestamp": true}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, v := range x {
				if keys[k] {
					out[k] = clean(v)
				}
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, v := range x {
				out[i] = clean(v)
			}
			return out
		default:
			return v
		}
	}
	return clean(input).(map[string]any)
}
