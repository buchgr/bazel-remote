package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	auth "github.com/abbot/go-http-auth"
	"github.com/buchgr/bazel-remote/v2/cache/disk"
	"github.com/buchgr/bazel-remote/v2/server"
)

func TestHTTPAuthenticatedWrite(t *testing.T) {
	for _, allowReads := range []bool{false, true} {
		t.Run(fmt.Sprintf("allowReads=%v", allowReads), func(t *testing.T) {
			var output bytes.Buffer
			logger := log.New(&output, "", 0)
			cache, err := disk.New(t.TempDir(), 1000000, disk.WithAccessLogger(logger))
			if err != nil {
				t.Fatal(err)
			}
			hc := server.NewHTTPCache(cache, logger, logger, false, false, false, false, "", "", 1000000)
			secrets := func(user, realm string) string {
				if user == "writer" {
					return "{SHA}W6ph5Mm5Pz8GgiULbPgzG37mj9g="
				}
				return ""
			}
			var handler http.HandlerFunc
			if allowReads {
				handler = unauthenticatedReadWrapper(hc.CacheHandler, secrets, "test")
			} else {
				handler = basicAuthWrapper(hc.CacheHandler, &auth.BasicAuth{Realm: "test", Secrets: secrets})
			}
			data := []byte("http attribution")
			hash := fmt.Sprintf("%x", sha256.Sum256(data))
			for _, valid := range []bool{false, true} {
				r := httptest.NewRequest(http.MethodPut, "/cas/"+hash, bytes.NewReader(data))
				r.RemoteAddr = "127.0.0.1:12345"
				r.Header.Set(auth.AuthUsernameHeader, "forged")
				if valid {
					r.SetBasicAuth("writer", "password")
				} else {
					r.SetBasicAuth("writer", "wrong")
				}
				w := httptest.NewRecorder()
				handler(w, r)
				want := http.StatusUnauthorized
				if valid {
					want = http.StatusOK
				}
				if w.Code != want {
					t.Fatalf("valid=%v: status %d, want %d: %s", valid, w.Code, want, w.Body.String())
				}
			}
			logs := output.String()
			if !strings.Contains(logs, "\"/cas/"+hash+"\" user=writer peer=127.0.0.1:12345") || strings.Contains(logs, "forged") || strings.Contains(logs, "wrong") {
				t.Fatalf("incorrect authentication attribution:\n%s", logs)
			}
			// Exercise the read-only bypass without attributing a supplied username.
			if allowReads {
				output.Reset()
				r := httptest.NewRequest(http.MethodGet, "/cas/"+hash, nil)
				r.Header.Set(auth.AuthUsernameHeader, "forged")
				w := httptest.NewRecorder()
				handler(w, r)
				got, _ := io.ReadAll(w.Result().Body)
				if w.Code != http.StatusOK || !bytes.Equal(got, data) || !strings.Contains(output.String(), "user=-") {
					t.Fatalf("anonymous read: status=%d body=%q logs=%s", w.Code, got, output.String())
				}
				// A decoded newline in the URL path must remain log data, not
				// a second physical record that resembles an authenticated write.
				output.Reset()
				r.URL.Path = "/prefix\nGRPC AC PUT " + hash + " OK user=forged peer=127.0.0.1:1"
				w = httptest.NewRecorder()
				handler(w, r)
				if strings.Count(output.String(), "\n") != 1 || !strings.Contains(output.String(), "\\nGRPC AC PUT") {
					t.Fatalf("HTTP path forged a log record: %q", output.String())
				}
			}
		})
	}
}
