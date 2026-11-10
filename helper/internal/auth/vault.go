package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Credentials are only serialized inside the OS-encrypted vault, never to RPC.
type Credentials struct {
	AccountID string `json:"accountId,omitempty"`
	User      string `json:"user"`
	Developer string `json:"developer"`
	Country   string `json:"country"`
	SignedOut bool   `json:"signedOut,omitempty"`
}
type Vault interface {
	Load() (Credentials, error)
	Save(Credentials) error
}
type FileVault struct{ Path string }

func (v FileVault) Load() (Credentials, error) {
	var c Credentials
	data, err := os.ReadFile(v.Path)
	if err != nil {
		return c, err
	}
	data, err = unseal(data)
	if err != nil {
		return c, errors.New("cannot unlock the saved Apple Podcasts login with this OS account")
	}
	if json.Unmarshal(data, &c) != nil {
		return c, errors.New("the saved Apple Podcasts login is unreadable")
	}
	return c, nil
}
func (v FileVault) Save(c Credentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	encrypted, err := seal(data)
	if err != nil {
		return err
	}
	dir := filepath.Dir(v.Path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "session-*.tmp")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(encrypted)
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
	return os.Rename(path, v.Path)
}
