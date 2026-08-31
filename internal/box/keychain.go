package box

import "errors"

// KeychainLookup reads a generic password (service = name) from the macOS
// Keychain. Failures are one of the sentinel errors below so callers can tell
// the user the right remedy: a missing item is added; an unreadable one
// needs an ACL entry (`-A` / `-T`) or an unlocked login keychain, which only a
// GUI session has — env_file is the unattended path.
var KeychainLookup = keychainLookup

var (
	// ErrKeychainNotFound: no item with that service name (security exit 44).
	ErrKeychainNotFound = errors.New("no Keychain item with that service name")
	// ErrKeychainNoInteraction: the keychain is locked or there is no GUI
	// session to unlock it — ssh, launchd, cron (security exit 36).
	ErrKeychainNoInteraction = errors.New("the login keychain cannot be unlocked here (no GUI session, or it is locked)")
	// ErrKeychainDenied: the item exists but its access control list does
	// not include this binary and no user is there to allow it (exit 51).
	ErrKeychainDenied = errors.New("the Keychain item exists but this process may not read it (no ACL entry)")
)

// KeychainRemedy is the command that fixes err for the variable name.
func KeychainRemedy(name string, err error) string {
	switch {
	case errors.Is(err, ErrKeychainNotFound):
		return "add one: security add-generic-password -a \"$USER\" -s " + name + " -w '<secret>' -U"
	case errors.Is(err, ErrKeychainNoInteraction):
		return "log in to the Mac's GUI session once (the login keychain unlocks there), or use env_file for an unattended host"
	case errors.Is(err, ErrKeychainDenied):
		return "grant access: security add-generic-password -a \"$USER\" -s " + name + " -w '<secret>' -U -A   (or -T /path/to/corral); do not add a second item"
	default:
		return "check: security find-generic-password -s " + name + " -w"
	}
}
