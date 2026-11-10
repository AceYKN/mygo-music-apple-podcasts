//go:build !windows

package auth

import "errors"

func seal([]byte) ([]byte, error) {
	return nil, errors.New("secure login persistence is currently available on Windows only")
}
func unseal([]byte) ([]byte, error) {
	return nil, errors.New("secure login persistence is currently available on Windows only")
}
