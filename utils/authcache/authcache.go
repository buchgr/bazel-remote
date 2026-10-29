// Package authcache checks basic auth credentials against an htpasswd
// file, remembering the ones that passed. A client that sends the same
// credentials on every request then pays for a slow password hash
// (eg bcrypt) once, not on every request.
package authcache

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"net/http"
	"sync"

	auth "github.com/abbot/go-http-auth"
)

// Checker verifies passwords against an auth.SecretProvider.
//
// Only successful checks are remembered, at most one per user, together
// with the stored secret that the password was verified against. An entry
// only counts while that secret is unchanged, so a changed or removed
// htpasswd entry takes effect on the next request. A stale entry stays in
// memory until the user next passes a check. A wrong password always gets
// a full check.
type Checker struct {
	secrets auth.SecretProvider
	compare func(password, secret string) bool

	// Passwords are remembered as an HMAC under a random per-process key,
	// not in plain text. Anyone who can read this process's memory can
	// still test password guesses against it at HMAC-SHA256 speed.
	key [sha256.Size]byte

	mu       sync.RWMutex
	verified map[string]verifiedPassword
}

type verifiedPassword struct {
	secret string
	mac    [sha256.Size]byte
}

// New returns a Checker that looks up users' secrets with secrets, and
// remembers the passwords that passed.
func New(secrets auth.SecretProvider) *Checker {
	c := &Checker{
		secrets:  secrets,
		compare:  auth.CheckSecret,
		verified: make(map[string]verifiedPassword),
	}
	_, _ = rand.Read(c.key[:]) // Never fails since Go 1.24.
	return c
}

// Check reports whether password is the password of username.
func (c *Checker) Check(username, password string) bool {
	secret := c.secrets(username, "")
	if secret == "" {
		return false // User does not exist.
	}

	c.mu.RLock()
	v, cached := c.verified[username]
	c.mu.RUnlock()

	mac := c.mac(password)
	if cached && v.secret == secret && hmac.Equal(v.mac[:], mac[:]) {
		return true
	}

	if !c.compare(password, secret) {
		return false
	}

	c.mu.Lock()
	c.verified[username] = verifiedPassword{secret: secret, mac: mac}
	c.mu.Unlock()

	return true
}

func (c *Checker) mac(password string) [sha256.Size]byte {
	h := hmac.New(sha256.New, c.key[:])
	h.Write([]byte(password))
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// BasicAuth is an auth.BasicAuth that checks credentials with a Checker.
//
// The NewContext method is inherited from auth.BasicAuth and does not use
// the Checker: go-http-auth keeps its context key private, so an override
// could not produce a context that auth.FromContext reads. bazel-remote
// only uses Wrap, via auth.JustCheck.
type BasicAuth struct {
	*auth.BasicAuth
	checker *Checker
}

// NewBasicAuth returns a BasicAuth for realm that checks credentials
// with checker.
func NewBasicAuth(realm string, checker *Checker) *BasicAuth {
	return &BasicAuth{
		BasicAuth: &auth.BasicAuth{Realm: realm, Secrets: checker.secrets},
		checker:   checker,
	}
}

// CheckAuth returns the username if r carries valid basic auth
// credentials, and "" otherwise.
func (a *BasicAuth) CheckAuth(r *http.Request) string {
	username, password, ok := r.BasicAuth()
	if !ok || !a.checker.Check(username, password) {
		return ""
	}
	return username
}

// Wrap is auth.BasicAuth.Wrap, using the CheckAuth method above.
func (a *BasicAuth) Wrap(wrapped auth.AuthenticatedHandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := a.CheckAuth(r)
		if username == "" {
			a.RequireAuth(w, r)
			return
		}
		wrapped(w, &auth.AuthenticatedRequest{Request: *r, Username: username})
	}
}
