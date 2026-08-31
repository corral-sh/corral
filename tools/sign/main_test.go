package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateSignVerify(t *testing.T) {
	dir := t.TempDir()
	pub, secret, file := filepath.Join(dir, "k.pub"), filepath.Join(dir, "k.key"), filepath.Join(dir, "SHA256SUMS")
	if err := generate(pub, secret); err != nil {
		t.Fatal(err)
	}
	if err := generate(pub, secret); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Error("a second -gen must not overwrite the public key")
	}
	key, err := os.ReadFile(secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(string(key)), "\n") != 0 {
		t.Error("secret key must be one line (a masked CI variable)")
	}
	if err := os.WriteFile(file, []byte("abc  corral-darwin-arm64\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := signFile(file, pub, string(key), "0.0.0-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(file, pub); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(file, pub); err == nil {
		t.Error("tampered file must fail verification")
	}
	if _, err := keyFromEnv(""); err == nil {
		t.Error("empty key must be an error that explains protected refs")
	}
	// A different key pair must be refused against this public key.
	pub2, secret2 := filepath.Join(dir, "k2.pub"), filepath.Join(dir, "k2.key")
	if err := generate(pub2, secret2); err != nil {
		t.Fatal(err)
	}
	key2, _ := os.ReadFile(secret2)
	if err := signFile(file, pub, string(key2), ""); err == nil || !strings.Contains(err.Error(), "not the public key") {
		t.Errorf("mismatched key must be refused: %v", err)
	}
}
