package syncstate

import (
	"encoding/json"
	"strings"
	"testing"
)

const accountA = "aaaaaaaaaaaaaaaaaaaaaaaa"
const accountB = "bbbbbbbbbbbbbbbbbbbbbbbb"

func TestPendingChangesSurviveRestartAndRemainAccountScoped(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	first, err := s.Queue(accountA, "podcasts", "42", true)
	if err != nil {
		t.Fatal(err)
	}
	last, err := s.Queue(accountA, "podcasts", "42", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == last.ID {
		t.Fatal("operation generation was reused")
	}
	if err = s.Resolve(accountA, last.ID, "network"); err != nil {
		t.Fatal(err)
	}
	restarted := New(dir)
	state, err := restarted.Read(accountA)
	if err != nil || len(state.Pending) != 1 || state.Pending[0].On || state.Pending[0].Error != "network" {
		t.Fatalf("unconfirmed intent lost: %+v %v", state, err)
	}
	other, err := restarted.Read(accountB)
	if err != nil || len(other.Pending) != 0 {
		t.Fatal("account data leaked")
	}
	if err = restarted.Resolve(accountA, first.ID, ""); err != nil {
		t.Fatal(err)
	}
	state, _ = restarted.Read(accountA)
	if len(state.Pending) != 1 {
		t.Fatal("late success discarded a newer intent")
	}
	if err = restarted.Resolve(accountA, last.ID, ""); err != nil {
		t.Fatal(err)
	}
	state, _ = restarted.Read(accountA)
	if len(state.Pending) != 0 {
		t.Fatal("confirmed operation retained")
	}
}
func TestSnapshotsRetainDisplayMetadataWithoutPlaybackSecrets(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Verify(accountA); err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"data": []any{map[string]any{"id": "42", "type": "podcast-episodes", "attributes": map[string]any{"name": "Example", "assetUrl": "private-signed-url", "offers": []any{"private-offer"}, "media-user-token": "credential", "durationInMilliseconds": 30000}, "relationships": map[string]any{"playback-position": map[string]any{"data": []any{map[string]any{"attributes": map[string]any{"completed": false, "positionInMilliseconds": 1000}}}}}}}}
	if err := s.Cache(accountA, "/v1/example", data); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read(accountA)
	if err != nil || !state.Verified || state.Snapshots["/v1/example"].At == "" {
		t.Fatal("snapshot not saved")
	}
	encoded, _ := json.Marshal(state)
	for _, secret := range []string{"private-signed-url", "private-offer", "credential", "assetUrl", "offers"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("snapshot contains %s", secret)
		}
	}
	if !strings.Contains(string(encoded), "positionInMilliseconds") {
		t.Fatal("cloud position missing")
	}
	if err = s.Clear(accountA); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Read(accountA)
	if state.Verified || len(state.Snapshots) != 0 {
		t.Fatal("sign-out data retained")
	}
	if _, err = s.Read("../../outside"); err == nil {
		t.Fatal("untrusted account namespace accepted")
	}
}
