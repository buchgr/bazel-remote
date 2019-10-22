package ldap

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/buchgr/bazel-remote/config"

	auth "github.com/abbot/go-http-auth"
	ldap "gopkg.in/ldap.v3"
)

// Cache represents a cache of LDAP query results so that many concurrent
// requests don't DDoS the LDAP server.
type Cache struct {
	*auth.BasicAuth
	m      sync.Map
	config *config.LDAPConfig
}

type cacheEntry struct {
	sync.Mutex
	// Poor man's enum; nil pointer means uninitialized
	authed *bool
}

func New(config *config.LDAPConfig) (*Cache, error) {
	conn, err := ldap.DialURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Test the configured bind credentials
	if err = conn.Bind(config.BindUser, config.BindPassword); err != nil {
		return nil, err
	}
	return &Cache{
		config: config,
		BasicAuth: &auth.BasicAuth{
			Realm: "Bazel remote cache",
		},
	}, nil
}

// Either query LDAP for a result or retrieve it from the cache
func (c *Cache) checkLdap(user, password string) bool {
	k := [2]string{user, password}
	v, _ := c.m.LoadOrStore(k, &cacheEntry{})
	ce := v.(*cacheEntry)
	ce.Lock()
	defer ce.Unlock()
	if ce.authed != nil {
		return *ce.authed
	}
	// Not initialized; actually do the query and record the result
	authed := c.query(user, password)
	ce.authed = &authed
	timeout := c.config.CacheTime
	// Don't cache a negative result for a long time; likely wrong password
	if !authed {
		timeout = 5 * time.Second
	}
	go func() {
		<-time.After(timeout)
		c.m.Delete(k)
	}()
	return authed
}

func (c *Cache) query(user, password string) bool {
	// This should always succeed since it was tested at instantiation
	conn, err := ldap.DialURL(c.config.BaseURL)
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	if err = conn.Bind(c.config.BindUser, c.config.BindPassword); err != nil {
		panic(err)
	}

	var groupsQuery strings.Builder
	if len(c.config.Groups) != 0 {
		groupsQuery.WriteString("(|")
		for _, group := range c.config.Groups {
			fmt.Fprintf(&groupsQuery, "(memberOf=%s)", group)
		}
		groupsQuery.WriteString(")")
	}

	// Does the user exist?
	query := fmt.Sprintf("(&(%s=%s)%s)", c.config.UsernameAttribute, user, groupsQuery.String())
	searchRequest := ldap.NewSearchRequest(
		c.config.BaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		query, []string{"dn"}, nil,
	)
	sr, err := conn.Search(searchRequest)
	if err != nil || len(sr.Entries) != 1 {
		return false
	}
	// Do they have the right credentials?
	return conn.Bind(sr.Entries[0].DN, password) == nil
}

// Below mostly copied from github.com/abbot/go-http-auth
// in order to "override" CheckAuth

func (c *Cache) CheckAuth(r *http.Request) string {
	s := strings.SplitN(r.Header.Get(c.Headers.V().Authorization), " ", 2)
	if len(s) != 2 || s[0] != "Basic" {
		return ""
	}

	b, err := base64.StdEncoding.DecodeString(s[1])
	if err != nil {
		return ""
	}
	pair := strings.SplitN(string(b), ":", 2)
	if len(pair) != 2 {
		return ""
	}
	user, password := pair[0], pair[1]
	if !c.checkLdap(user, password) {
		return ""
	}
	return user
}

func (c *Cache) Wrap(wrapped auth.AuthenticatedHandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if username := c.CheckAuth(r); username == "" {
			c.RequireAuth(w, r)
		} else {
			ar := &auth.AuthenticatedRequest{Request: *r, Username: username}
			wrapped(w, ar)
		}
	}
}

type key int

var infoKey key

func (c *Cache) NewContext(ctx context.Context, r *http.Request) context.Context {
	info := &auth.Info{Username: c.CheckAuth(r), ResponseHeaders: make(http.Header)}
	info.Authenticated = info.Username != ""
	if !info.Authenticated {
		info.ResponseHeaders.Set(c.Headers.V().Authenticate, `Basic realm="`+c.Realm+`"`)
	}
	return context.WithValue(ctx, infoKey, info)
}
