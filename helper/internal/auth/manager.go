// Package auth preserves login independently from the browser used to obtain it.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/browser"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type LoginBrowser interface {
	Session() (browser.Session, error)
	SignIn() error
	SignOut() error
	Close()
}
type Manager struct {
	mu         sync.Mutex
	Vault      Vault
	Browser    func(interactive bool) LoginBrowser
	Developer  func(current string) (string, error)
	Migrate    bool
	Now        func() time.Time
	loaded     bool
	current    Credentials
	login      LoginBrowser
	repairing  *Credentials
	lastRepair time.Time
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
func session(c Credentials) browser.Session {
	hash := sha256.Sum256([]byte(c.User))
	id := c.AccountID
	if id == "" {
		id = hex.EncodeToString(hash[:12])
	}
	country := c.Country
	if len(country) != 2 {
		country = "us"
	}
	return browser.Session{ID: id, Authorized: c.User != "" && !c.SignedOut, Country: country, User: c.User, Developer: c.Developer}
}
func (m *Manager) save(c Credentials) error {
	if err := m.Vault.Save(c); err != nil {
		return errors.New("cannot securely save the Apple Podcasts login")
	}
	m.current = c
	m.loaded = true
	return nil
}
func (m *Manager) capture(interactive bool) (Credentials, error) {
	b := m.Browser(interactive)
	defer b.Close()
	s, err := b.Session()
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{User: s.User, Developer: s.Developer, Country: s.Country}, nil
}
func (m *Manager) load() error {
	if m.loaded {
		return nil
	}
	c, err := m.Vault.Load()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if os.IsNotExist(err) && m.Migrate {
		c, err = m.capture(false)
		if err != nil {
			return err
		}
		if err = m.save(c); err != nil {
			return err
		}
	}
	m.current = c
	m.loaded = true
	return nil
}
func (m *Manager) Session() (browser.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.load(); err != nil {
		return browser.Session{}, err
	}
	if m.login != nil {
		s, err := m.login.Session()
		if err != nil {
			return s, err
		}
		return s, nil
	}
	if m.repairing != nil {
		return session(*m.repairing), nil
	}
	if m.Developer != nil {
		token, err := m.Developer(m.current.Developer)
		if err != nil && m.current.Developer == "" {
			return browser.Session{}, err
		}
		if token != "" && token != m.current.Developer {
			c := m.current
			c.Developer = token
			if err = m.save(c); err != nil {
				return browser.Session{}, err
			}
		}
	}
	return session(m.current), nil
}

// Confirm is called only after the account API accepted these exact credentials.
func (m *Manager) Confirm(s browser.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := Credentials{AccountID: s.ID, User: s.User, Developer: s.Developer, Country: s.Country}
	if c != m.current {
		if err := m.save(c); err != nil {
			return err
		}
	}
	m.repairing = nil
	if m.login != nil {
		m.login.Close()
		m.login = nil
	}
	return nil
}
func (m *Manager) SignIn() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repairing = nil
	if m.login != nil {
		m.login.Close()
	}
	m.login = m.Browser(true)
	return m.login.SignIn()
}

// Repair silently reuses the persistent official login, at most once per ten
// minutes and only after the account API rejected authorization. It never runs
// for timeouts, rate limits or general network failures.
func (m *Manager) Repair() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.load(); err != nil {
		return false, err
	}
	if m.current.SignedOut || m.login != nil {
		return false, nil
	}
	if !m.lastRepair.IsZero() && m.now().Sub(m.lastRepair) < 10*time.Minute {
		return false, nil
	}
	m.lastRepair = m.now()
	c, err := m.capture(false)
	if err != nil {
		return false, err
	}
	if c.User == "" {
		return false, nil
	}
	changed := c != m.current
	if changed {
		m.repairing = &c
	}
	return changed, nil
}

func (m *Manager) CancelRepair() { m.mu.Lock(); defer m.mu.Unlock(); m.repairing = nil }
func (m *Manager) SignOut() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.load(); err != nil {
		return err
	}
	// The explicit tombstone prevents a preserved legacy profile from silently
	// signing the account back in on the next restart.
	c := m.current
	m.repairing = nil
	c.User = ""
	c.AccountID = ""
	c.SignedOut = true
	if err := m.save(c); err != nil {
		return err
	}
	if m.login != nil {
		m.login.Close()
		m.login = nil
	}
	return nil
}

// Like the NetEase plugin's active-session check, a response may renew only the
// exact account that sent it. A late response cannot replace a newer login.
func (m *Manager) AcceptResponse(sent browser.Session, header http.Header) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.loaded || m.login != nil || m.current.User == "" || m.current.User != sent.User || m.current.SignedOut {
		return nil
	}
	token := header.Get("media-user-token")
	for _, cookie := range (&http.Response{Header: header}).Cookies() {
		domain := strings.TrimPrefix(cookie.Domain, ".")
		if cookie.Name == "media-user-token" && cookie.Value != "" && cookie.MaxAge >= 0 && (cookie.Expires.IsZero() || cookie.Expires.After(m.now())) && (domain == "" || domain == "apple.com" || strings.HasSuffix(domain, ".apple.com")) {
			token = cookie.Value
		}
	}
	if token == "" || token == m.current.User {
		return nil
	}
	c := m.current
	c.AccountID = sent.ID
	c.User = token
	return m.save(c)
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.login != nil {
		m.login.Close()
		m.login = nil
	}
}
func (m *Manager) Diagnostics() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]any{"browserRunning": m.login != nil, "savedLogin": m.loaded && m.current.User != "", "chromePlayback": false}
}
