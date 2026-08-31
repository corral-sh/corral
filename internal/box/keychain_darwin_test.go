package box

import (
	"errors"
	"testing"
)

func TestKeychainErrorCodes(t *testing.T) {
	if !errors.Is(keychainError(44, "The specified item could not be found in the keychain."), ErrKeychainNotFound) {
		t.Error("44 is item not found")
	}
	if !errors.Is(keychainError(36, "User interaction is not allowed."), ErrKeychainNoInteraction) {
		t.Error("36 is interaction not allowed")
	}
	if !errors.Is(keychainError(51, "security: SecKeychainSearchCopyNext: The authorization was denied"), ErrKeychainDenied) {
		t.Error("51 is denied")
	}
	if err := keychainError(1, "something else"); err.Error() != "something else" {
		t.Errorf("unknown code keeps security's message: %v", err)
	}
}
