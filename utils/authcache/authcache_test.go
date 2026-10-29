package authcache

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	auth "github.com/abbot/go-http-auth"
)

// bcrypt hashes at cost 4, from `htpasswd -nbB -C 4 u <password>`.
const (
	password1 = "correct-horse"
	hash1     = "$2y$04$OdIHfkOiKIewtUfKmiCpBejXsSIdEBTwMuR6KcInCnrAn9Xy1UCui"
	password2 = "other-battery"
	hash2     = "$2y$04$ESxayWjCo7hZF6SlM7qQVOobPjzTrS4/Qn8HUyiP4/aWw7mjcijLS"
)

// secretsMap is an in-memory auth.SecretProvider whose entries can change.
type secretsMap struct {
	mu sync.Mutex
	m  map[string]string
}

func newSecretsMap(kv ...string) *secretsMap {
	s := &secretsMap{m: make(map[string]string)}
	for i := 0; i < len(kv); i += 2 {
		s.m[kv[i]] = kv[i+1]
	}
	return s
}

func (s *secretsMap) set(user, secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if secret == "" {
		delete(s.m, user)
		return
	}
	s.m[user] = secret
}

func (s *secretsMap) provider(user, realm string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[user]
}

// newCountingChecker returns a Checker over secrets, and a count of the
// full password hash comparisons it has made.
func newCountingChecker(secrets auth.SecretProvider) (*Checker, *atomic.Int64) {
	c := New(secrets)
	return c, countCompares(c)
}

func countCompares(c *Checker) *atomic.Int64 {
	var n atomic.Int64
	c.compare = func(password, secret string) bool {
		n.Add(1)
		return auth.CheckSecret(password, secret)
	}
	return &n
}

func expectCheck(t *testing.T, c *Checker, user, password string, want bool) {
	t.Helper()
	if got := c.Check(user, password); got != want {
		t.Fatalf("Check(%q, %q) = %v, want %v", user, password, got, want)
	}
}

func expectCompares(t *testing.T, n *atomic.Int64, want int64) {
	t.Helper()
	if got := n.Load(); got != want {
		t.Fatalf("made %d full comparisons, want %d", got, want)
	}
}

func expectCachedUsers(t *testing.T, c *Checker, want int) {
	t.Helper()
	c.mu.RLock()
	got := len(c.verified)
	c.mu.RUnlock()
	if got != want {
		t.Fatalf("%d users cached, want %d", got, want)
	}
}

func TestCheckCachesValidPassword(t *testing.T) {
	c, n := newCountingChecker(newSecretsMap("u", hash1).provider)

	for i := 0; i < 3; i++ {
		expectCheck(t, c, "u", password1, true)
	}
	expectCompares(t, n, 1)
}

func TestCheckNeverCachesWrongPassword(t *testing.T) {
	c, n := newCountingChecker(newSecretsMap("u", hash1).provider)

	for i := 0; i < 3; i++ {
		expectCheck(t, c, "u", "wrong", false)
	}
	expectCompares(t, n, 3)

	// A remembered valid password must not let a wrong one through, and
	// wrong passwords must not evict it.
	expectCheck(t, c, "u", password1, true)
	expectCheck(t, c, "u", "wrong", false)
	expectCheck(t, c, "u", "", false)
	expectCheck(t, c, "u", password1, true)
	expectCompares(t, n, 6)
}

func TestCheckUnknownUser(t *testing.T) {
	c, n := newCountingChecker(newSecretsMap("u", hash1).provider)

	expectCheck(t, c, "nobody", password1, false)
	expectCheck(t, c, "", "", false)
	expectCompares(t, n, 0)
	expectCachedUsers(t, c, 0)
}

func TestCheckIsPerUser(t *testing.T) {
	c, _ := newCountingChecker(newSecretsMap("a", hash1, "b", hash2).provider)

	expectCheck(t, c, "a", password1, true)
	expectCheck(t, c, "b", password2, true)

	expectCheck(t, c, "a", password2, false)
	expectCheck(t, c, "b", password1, false)
}

func TestCheckFollowsChangedSecret(t *testing.T) {
	s := newSecretsMap("u", hash1)
	c, n := newCountingChecker(s.provider)

	expectCheck(t, c, "u", password1, true)

	s.set("u", hash2)
	expectCheck(t, c, "u", password1, false)
	expectCheck(t, c, "u", password2, true)
	expectCheck(t, c, "u", password2, true)
	expectCompares(t, n, 3)
	expectCachedUsers(t, c, 1)
}

func TestCheckRejectsRemovedUser(t *testing.T) {
	s := newSecretsMap("u", hash1)
	c, n := newCountingChecker(s.provider)

	expectCheck(t, c, "u", password1, true)

	s.set("u", "")
	expectCheck(t, c, "u", password1, false)
	expectCompares(t, n, 1)

	// The entry was verified against this same secret, so it is valid again.
	s.set("u", hash1)
	expectCheck(t, c, "u", password1, true)
	expectCompares(t, n, 1)
	expectCheck(t, c, "u", "wrong", false)
	expectCompares(t, n, 2)
}

// A rotation that lands while a full comparison is running must not leave
// the old password usable.
func TestCheckRotationDuringCheck(t *testing.T) {
	s := newSecretsMap("u", hash1)
	c := New(s.provider)

	comparing := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	c.compare = func(password, secret string) bool {
		once.Do(func() {
			close(comparing)
			<-release
		})
		return auth.CheckSecret(password, secret)
	}

	result := make(chan bool)
	go func() { result <- c.Check("u", password1) }()

	<-comparing
	s.set("u", hash2)
	close(release)

	// The in-flight check read the secret before the rotation.
	if !<-result {
		t.Fatal("in-flight check of the pre-rotation password failed")
	}

	expectCheck(t, c, "u", password1, false)
	expectCheck(t, c, "u", password2, true)
}

func TestCheckFollowsHtpasswdFileReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "htpasswd")
	write := func(content string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		// The provider reloads on an mtime change, which can be too
		// coarse to see two writes in a row.
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now()
	write("u:"+hash1+"\n", now)
	c := New(auth.HtpasswdFileProvider(path))
	expectCheck(t, c, "u", password1, true)

	write("u:"+hash2+"\n", now.Add(time.Hour))
	expectCheck(t, c, "u", password1, false)
	expectCheck(t, c, "u", password2, true)

	write("other:"+hash1+"\n", now.Add(2*time.Hour))
	expectCheck(t, c, "u", password2, false)
}

func TestCheckConcurrent(t *testing.T) {
	c, _ := newCountingChecker(newSecretsMap("u", hash1).provider)

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if (g+i)%3 == 0 {
					if c.Check("u", "wrong") {
						t.Error("wrong password accepted")
					}
				} else if !c.Check("u", password1) {
					t.Error("valid password rejected")
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestBasicAuthWrap(t *testing.T) {
	c, n := newCountingChecker(newSecretsMap("u", hash1).provider)

	var gotUser string
	handler := auth.JustCheck(NewBasicAuth("realm", c), func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get(auth.AuthUsernameHeader)
		w.WriteHeader(http.StatusNoContent)
	})

	serve := func(setAuth func(r *http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/cas/0", nil)
		setAuth(r)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}

	unauthorized := []struct {
		name    string
		setAuth func(r *http.Request)
	}{
		{"no credentials", func(r *http.Request) {}},
		{"wrong password", func(r *http.Request) { r.SetBasicAuth("u", "wrong") }},
		{"unknown user", func(r *http.Request) { r.SetBasicAuth("nobody", password1) }},
		{"not basic", func(r *http.Request) { r.Header.Set("Authorization", "Bearer x") }},
	}
	for _, tc := range unauthorized {
		w := serve(tc.setAuth)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want %d", tc.name, w.Code, http.StatusUnauthorized)
		}
		if got, want := w.Header().Get("WWW-Authenticate"), `Basic realm="realm"`; got != want {
			t.Errorf("%s: WWW-Authenticate %q, want %q", tc.name, got, want)
		}
	}

	for i := 0; i < 2; i++ {
		w := serve(func(r *http.Request) { r.SetBasicAuth("u", password1) })
		if w.Code != http.StatusNoContent {
			t.Fatalf("valid credentials: status %d, want %d", w.Code, http.StatusNoContent)
		}
		if gotUser != "u" {
			t.Fatalf("handler saw user %q, want %q", gotUser, "u")
		}
	}

	// One comparison for the wrong password, one for the first valid
	// request. The second valid request is served from the cache.
	expectCompares(t, n, 2)
}

func BenchmarkCheck(b *testing.B) {
	secrets := newSecretsMap("u", hash1).provider

	b.Run("uncached", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			auth.CheckSecret(password1, hash1)
		}
	})

	b.Run("cached", func(b *testing.B) {
		c := New(secrets)
		c.Check("u", password1)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Check("u", password1)
		}
	})

	// Includes the os.Stat that HtpasswdFileProvider makes on every lookup.
	b.Run("cached-htpasswd-file", func(b *testing.B) {
		path := filepath.Join(b.TempDir(), "htpasswd")
		if err := os.WriteFile(path, []byte("u:"+hash1+"\n"), 0o600); err != nil {
			b.Fatal(err)
		}
		c := New(auth.HtpasswdFileProvider(path))
		c.Check("u", password1)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Check("u", password1)
		}
	})
}
