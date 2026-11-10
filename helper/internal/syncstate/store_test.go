package syncstate

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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

func TestProgressJournalPreservesNewerBackwardSeekAndHoldsConflict(t *testing.T) {
	s := New(t.TempDir())
	at := time.Unix(1800000000, 0).UTC()
	old, _, err := s.QueuePosition(accountA, "42", 120000, 600000, false, at.Format(time.RFC3339Nano), false)
	if err != nil {
		t.Fatal(err)
	}
	latest, accepted, err := s.QueuePosition(accountA, "42", 30000, 600000, false, at.Add(time.Second).Format(time.RFC3339Nano), false)
	if err != nil || !accepted {
		t.Fatal(err)
	}
	s.Resolve(accountA, old.ID, "")
	state, _ := s.Read(accountA)
	if len(state.Pending) != 1 || state.Pending[0].Position != 30000 {
		t.Fatal("older response lost backward intent")
	}
	s.Resolve(accountA, latest.ID, "conflict")
	_, accepted, err = s.QueuePosition(accountA, "42", 32000, 600000, false, at.Add(2*time.Second).Format(time.RFC3339Nano), false)
	if err != nil || accepted {
		t.Fatal("periodic update silently replaced conflict")
	}
	_, accepted, err = s.QueuePosition(accountA, "42", 30000, 600000, false, at.Add(3*time.Second).Format(time.RFC3339Nano), true)
	if err != nil || !accepted {
		t.Fatal("explicit user retry was refused")
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
