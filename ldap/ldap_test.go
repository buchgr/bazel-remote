package ldap

import (
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/v2/config"
)

func TestCacheTimeout(t *testing.T) {
	c := &Cache{config: &config.LDAPConfig{CacheTime: time.Hour}}

	if got := c.cacheTimeout(true); got != time.Hour {
		t.Errorf("Expected a successful login to be cached for %v, got %v", time.Hour, got)
	}

	if got := c.cacheTimeout(false); got != 5*time.Second {
		t.Errorf("Expected a failed login to be cached for %v, got %v", 5*time.Second, got)
	}
}
