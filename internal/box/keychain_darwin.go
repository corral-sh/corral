package box

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func keychainLookup(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // the user may get a Keychain prompt
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", name, "-w")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", keychainError(ee.ExitCode(), stderr.String())
		}
		return "", fmt.Errorf("security: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// keychainError maps `security`'s exit status (the low byte of the OSStatus:
// -25300 item not found → 44, -25308 interaction not allowed → 36, -25293
// auth failed → 51) to the sentinel errors.
func keychainError(code int, stderr string) error {
	switch code {
	case 44:
		return ErrKeychainNotFound
	case 36:
		return ErrKeychainNoInteraction
	case 51:
		return ErrKeychainDenied
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = fmt.Sprintf("security exited %d", code)
	}
	return errors.New(msg)
}
