// Command sign produces and checks the minisign signature of a release's
// SHA256SUMS. It exists so the pipeline needs no minisign binary and no
// password prompt: the secret key is one line in a protected, masked CI
// variable; the public key is committed as release/minisign.pub and printed in
// the release notes. Testers verify with the reference tool
// (`brew install minisign`) or with `go run ./tools/sign -verify`.
//
//	go run ./tools/sign -gen -pub release/minisign.pub -secret <file>
//	CORRAL_MINISIGN_KEY=… go run ./tools/sign -sign -pub release/minisign.pub -comment "corral 0.6.0" dist/SHA256SUMS
//	go run ./tools/sign -verify -pub release/minisign.pub dist/SHA256SUMS
package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"aead.dev/minisign"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	gen := fs.Bool("gen", false, "generate a key pair: write -pub and -secret")
	sign := fs.Bool("sign", false, "sign <file> with the key in $CORRAL_MINISIGN_KEY (or -key-env), write <file>.minisig")
	verify := fs.Bool("verify", false, "verify <file> against <file>.minisig with -pub")
	pub := fs.String("pub", "release/minisign.pub", "public key file")
	secret := fs.String("secret", "", "-gen: where to write the secret key (one line, 0600) — never inside the repository")
	keyEnv := fs.String("key-env", "CORRAL_MINISIGN_KEY", "-sign: environment variable holding the secret key line")
	comment := fs.String("comment", "", "-sign: trusted comment (e.g. the version)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *gen:
		return generate(*pub, *secret)
	case *sign:
		if fs.NArg() != 1 {
			return errors.New("-sign needs exactly one file")
		}
		return signFile(fs.Arg(0), *pub, os.Getenv(*keyEnv), *comment)
	case *verify:
		if fs.NArg() != 1 {
			return errors.New("-verify needs exactly one file")
		}
		return verifyFile(fs.Arg(0), *pub)
	}
	fs.Usage()
	return errors.New("one of -gen, -sign, -verify is required")
}

func generate(pubPath, secretPath string) error {
	if secretPath == "" {
		return errors.New("-gen needs -secret <file> (outside the repository)")
	}
	if _, err := os.Stat(pubPath); err == nil {
		return fmt.Errorf("%s exists — refusing to overwrite the published key", pubPath)
	}
	pk, sk, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	pubText, err := pk.MarshalText()
	if err != nil {
		return err
	}
	skText, err := sk.MarshalText()
	if err != nil {
		return err
	}
	// The key file format is "untrusted comment: …\n<base64>"; the variable
	// holds the base64 line only, so it can be a masked single-line CI variable.
	if err := os.WriteFile(secretPath, []byte(lastLine(string(skText))+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(pubPath, append(pubText, '\n'), 0o644); err != nil { //nolint:gosec // a public key
		return err
	}
	fmt.Printf("public key %s written (key id %X); secret key in %s — put its single line in the protected+masked CI variable CORRAL_MINISIGN_KEY and keep a copy outside the repository\n", pubPath, pk.ID(), secretPath)
	return nil
}

// keyFromEnv accepts the secret key as our own unencrypted one-liner or as the
// two-line file format minisign(1) writes with an empty password.
func keyFromEnv(v string) (minisign.PrivateKey, error) {
	var sk minisign.PrivateKey
	v = strings.TrimSpace(v)
	if v == "" {
		return sk, errors.New("secret key variable is empty (protected variables are only available on protected refs — tags vX.Y.Z)")
	}
	if !strings.Contains(v, "\n") {
		v = "untrusted comment: corral release key\n" + v
	}
	if minisign.IsEncrypted([]byte(v)) {
		return minisign.DecryptKey("", []byte(v))
	}
	if err := sk.UnmarshalText([]byte(v)); err != nil {
		return sk, fmt.Errorf("secret key: %w", err)
	}
	return sk, nil
}

func signFile(path, pubPath, key, comment string) error {
	sk, err := keyFromEnv(key)
	if err != nil {
		return err
	}
	pk, err := minisign.PublicKeyFromFile(pubPath)
	if err != nil {
		return err
	}
	if !pk.Equal(sk.Public()) {
		return fmt.Errorf("%s is not the public key of the signing key (id %X vs %X)", pubPath, pk.ID(), sk.ID())
	}
	msg, err := os.ReadFile(path) //nolint:gosec // the file to sign, given on the command line
	if err != nil {
		return err
	}
	if comment == "" {
		comment = path
	}
	sig := minisign.SignWithComments(sk, msg, "corral "+comment, "signature from corral release key "+fmt.Sprintf("%X", pk.ID()))
	if !minisign.Verify(pk, msg, sig) {
		return errors.New("self-check failed: signature does not verify")
	}
	if err := os.WriteFile(path+".minisig", sig, 0o644); err != nil { //nolint:gosec // a signature
		return err
	}
	fmt.Printf("signed %s → %s.minisig (key id %X)\n", path, path, pk.ID())
	return nil
}

func verifyFile(path, pubPath string) error {
	pk, err := minisign.PublicKeyFromFile(pubPath)
	if err != nil {
		return err
	}
	msg, err := os.ReadFile(path) //nolint:gosec // the file to verify
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(path + ".minisig")
	if err != nil {
		return err
	}
	if !minisign.Verify(pk, msg, sig) {
		return fmt.Errorf("%s: signature does NOT verify with %s", path, pubPath)
	}
	fmt.Printf("%s: signature OK (key id %X)\n", path, pk.ID())
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
