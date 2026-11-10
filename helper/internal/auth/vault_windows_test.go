//go:build windows

package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsVaultDoesNotStorePlaintextAndReopens(t *testing.T) {
	v := FileVault{Path: filepath.Join(t.TempDir(), "session.bin")}
	c := Credentials{User: "private-test-user-123456789", Developer: "private-test-developer", Country: "jp"}
	if err := v.Save(c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(c.User)) || bytes.Contains(data, []byte(c.Developer)) {
		t.Fatal("plaintext login written")
	}
	got, err := (FileVault{Path: v.Path}).Load()
	if err != nil || got != c {
		t.Fatal("login did not survive a new vault instance", err)
	}
}
