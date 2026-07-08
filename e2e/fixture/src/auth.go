// Package auth is a fixture source file for the e2e suite. The auth/login
// node references this file, exercising source-hash staleness checks.
//
// IMPORTANT: e2e/fixture/vault/auth/login.md stores the SHA-256 of this
// file's exact bytes — if you edit this file, regenerate that hash
// (`shasum -a 256 e2e/fixture/src/auth.go`). This file is also compiled and
// linted as part of `./...`, so it must stay buildable and lint-clean.
package auth

// Login validates credentials and issues a session token.
func Login(user, pass string) (string, error) {
	return "token", nil
}
