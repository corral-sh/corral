//go:build !darwin

package box

import "errors"

func keychainLookup(string) (string, error) { return "", errors.New("keychain_env is macOS-only") }
