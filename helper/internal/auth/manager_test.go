package auth

import (
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/browser"
	"net/http"
	"os"
	"testing"
	"time"
)

type memoryVault struct {
	value  Credentials
	exists bool
}

func TestResponseRenewalKeepsIdentityAndIgnoresOldAccount(t *testing.T) {
	v := &memoryVault{exists: true, value: Credentials{User: "old", Developer: "public", Country: "jp"}}
	m := &Manager{Vault: v}
	before, err := m.Session()
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Add("Set-Cookie", "media-user-token=renewed; Domain=.apple.com; Path=/; Secure")
	if err = m.AcceptResponse(before, h); err != nil {
		t.Fatal(err)
	}
	after, err := m.Session()
	if err != nil || after.ID != before.ID || after.User != "renewed" {
		t.Fatal("renewal lost account identity", err)
	}
	late := http.Header{}
	late.Set("media-user-token", "stale-response")
	if err = m.AcceptResponse(before, late); err != nil {
		t.Fatal(err)
	}
	current, _ := m.Session()
	if current.User != "renewed" {
		t.Fatal("late response overwrote a newer session")
	}
}

func (v *memoryVault) Load() (Credentials, error) {
	if !v.exists {
		return Credentials{}, os.ErrNotExist
	}
	return v.value, nil
}
func (v *memoryVault) Save(c Credentials) error { v.exists = true; v.value = c; return nil }

type fakeBrowser struct {
	value  browser.Session
	closed *int
}

func (b fakeBrowser) Session() (browser.Session, error) { return b.value, nil }
func (b fakeBrowser) SignIn() error                     { return nil }
func (b fakeBrowser) SignOut() error                    { return nil }
func (b fakeBrowser) Close()                            { *b.closed++ }
func TestSavedLoginSurvivesRestartWithoutBrowser(t *testing.T) {
	v := &memoryVault{exists: true, value: Credentials{User: "synthetic-user", Developer: "synthetic-developer", Country: "jp"}}
	launches := 0
	factory := func(bool) LoginBrowser { launches++; return nil }
	for i := 0; i < 3; i++ {
		m := &Manager{Vault: v, Browser: factory, Migrate: true}
		for j := 0; j < 4; j++ {
			s, err := m.Session()
			if err != nil || !s.Authorized || s.User != "synthetic-user" {
				t.Fatal("saved login lost", err)
			}
		}
		m.Close()
	}
	if launches != 0 {
		t.Fatal("ordinary startup launched Chrome")
	}
}
func TestLegacyLoginMigratesOnceAndExplicitLogoutStaysOut(t *testing.T) {
	v := &memoryVault{}
	launches, closed := 0, 0
	factory := func(interactive bool) LoginBrowser {
		if interactive {
			t.Fatal("migration opened an interactive login")
		}
		launches++
		return fakeBrowser{value: browser.Session{User: "old-login", Developer: "public-config", Country: "jp"}, closed: &closed}
	}
	m := &Manager{Vault: v, Browser: factory, Migrate: true}
	if s, err := m.Session(); err != nil || !s.Authorized {
		t.Fatal("migration failed", err)
	}
	if launches != 1 || closed != 1 {
		t.Fatal("migration kept Chrome alive")
	}
	if err := m.SignOut(); err != nil {
		t.Fatal(err)
	}
	restart := &Manager{Vault: v, Browser: factory, Migrate: true}
	if s, err := restart.Session(); err != nil || s.Authorized {
		t.Fatal("logged back in after explicit logout")
	}
	if launches != 1 {
		t.Fatal("logout tombstone ignored")
	}
}
func TestRepairIsBoundedAndKeepsCredentials(t *testing.T) {
	v := &memoryVault{exists: true, value: Credentials{User: "old", Developer: "public", Country: "jp"}}
	launches, closed := 0, 0
	now := time.Unix(1800000000, 0)
	m := &Manager{Vault: v, Now: func() time.Time { return now }, Browser: func(bool) LoginBrowser {
		launches++
		return fakeBrowser{value: browser.Session{User: "renewed", Developer: "public", Country: "jp"}, closed: &closed}
	}}
	if changed, err := m.Repair(); err != nil || !changed {
		t.Fatal(err)
	}
	if changed, err := m.Repair(); err != nil || changed {
		t.Fatal("repeated repair", err)
	}
	if v.value.User != "old" {
		t.Fatal("unverified repair replaced the saved login")
	}
	candidate, _ := m.Session()
	if err := m.Confirm(candidate); err != nil {
		t.Fatal(err)
	}
	if launches != 1 || closed != 1 || v.value.User != "renewed" {
		t.Fatal("repair lost or repeatedly requested login")
	}
}
