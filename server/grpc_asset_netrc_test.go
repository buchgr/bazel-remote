package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchgr/bazel-remote/v2/cache/disk"
	asset "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/asset/v1"

	"google.golang.org/grpc/codes"

	testutils "github.com/buchgr/bazel-remote/v2/utils"
)

func parseNetrcForTest(t *testing.T, contents string) (map[string]netrcCredentials, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), ".netrc")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	logger := &recordingLogger{}
	creds, err := loadNetrcFile(path, logger)
	if err != nil {
		t.Fatal(err)
	}

	return creds, logger.joinedMessages()
}

func newTLSAssetFetchTestGRPCServer(t *testing.T, ts *httptest.Server, creds map[string]netrcCredentials) *grpcServer {
	t.Helper()

	diskCache, err := disk.New(t.TempDir(), 1024*1024, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	client := ts.Client()
	client.CheckRedirect = stripAuthorizationOnRedirect

	return &grpcServer{
		cache:            diskCache,
		accessLogger:     testutils.NewSilentLogger(),
		errorLogger:      testutils.NewSilentLogger(),
		netrcCredentials: creds,
		httpClient:       client,
	}
}

func newTLSAuthenticatedServer(t *testing.T, username, password string) (*httptest.Server, *string) {
	t.Helper()

	var gotAuthorization string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		u, p, ok := r.BasicAuth()
		if !ok || u != username || p != password {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		_, _ = w.Write([]byte("blob"))
	}))
	t.Cleanup(ts.Close)

	return ts, &gotAuthorization
}

func TestAssetFetchBlobUsesNetrcCredentialsOverHTTPS(t *testing.T) {
	ts, _ := newTLSAuthenticatedServer(t, "alice", "secret")
	u, _ := url.Parse(ts.URL)

	creds, _ := parseNetrcForTest(t, fmt.Sprintf("machine %s login alice password secret\n", u.Hostname()))
	srv := newTLSAssetFetchTestGRPCServer(t, ts, creds)

	resp, err := srv.FetchBlob(ctx, &asset.FetchBlobRequest{Uris: []string{ts.URL + "/blob"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status.GetCode() != int32(codes.OK) {
		t.Fatalf("expected successful fetch, got status %d", resp.Status.GetCode())
	}
}

func TestAssetFetchBlobDoesNotSendNetrcCredentialsOverHTTP(t *testing.T) {
	ts := newAuthenticatedTestGetServer("alice", "secret")
	defer ts.srv.Close()
	u, _ := url.Parse(ts.srv.URL)

	creds, _ := parseNetrcForTest(t, fmt.Sprintf("machine %s login alice password secret\n", u.Hostname()))
	diskCache, err := disk.New(t.TempDir(), 1024*1024, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	srv := &grpcServer{
		cache:            diskCache,
		accessLogger:     testutils.NewSilentLogger(),
		errorLogger:      testutils.NewSilentLogger(),
		netrcCredentials: creds,
		httpClient:       &http.Client{CheckRedirect: stripAuthorizationOnRedirect},
	}

	resp, err := srv.FetchBlob(ctx, &asset.FetchBlobRequest{Uris: []string{ts.srv.URL + "/" + ts.path}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status.GetCode() != int32(codes.NotFound) {
		t.Fatalf("expected the unauthenticated fetch to fail, got status %d", resp.Status.GetCode())
	}
}

func TestAssetFetchBlobDoesNotUseNetrcWhenAuthorizationQualifierExists(t *testing.T) {
	var gotAuthorization string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		if gotAuthorization != "Bearer token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		_, _ = w.Write([]byte("blob"))
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)

	creds, _ := parseNetrcForTest(t, fmt.Sprintf("machine %s login alice password secret\n", u.Hostname()))
	srv := newTLSAssetFetchTestGRPCServer(t, ts, creds)

	resp, err := srv.FetchBlob(ctx, &asset.FetchBlobRequest{
		Uris: []string{ts.URL + "/blob"},
		Qualifiers: []*asset.Qualifier{
			{Name: "http_header:authorization", Value: "Bearer token"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status.GetCode() != int32(codes.OK) {
		t.Fatalf("expected successful fetch, got status %d", resp.Status.GetCode())
	}
	if gotAuthorization != "Bearer token" {
		t.Fatalf("Authorization header = %q, want %q", gotAuthorization, "Bearer token")
	}
}

func TestStripAuthorizationOnRedirect(t *testing.T) {
	tests := []struct {
		name       string
		from, to   string
		wantHeader string
	}{
		{"same host", "https://repo.example/a", "https://repo.example/b", "Basic abc"},
		{"subdomain", "https://repo.example/a", "https://cdn.repo.example/b", ""},
		{"other host", "https://repo.example/a", "https://other.example/b", ""},
		{"other port", "https://repo.example/a", "https://repo.example:8443/b", ""},
		{"downgrade to http", "https://repo.example/a", "http://repo.example/b", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			via, _ := http.NewRequest(http.MethodGet, tc.from, nil)
			via.Header.Set("Authorization", "Basic abc")
			req, _ := http.NewRequest(http.MethodGet, tc.to, nil)
			req.Header.Set("Authorization", "Basic abc")

			if err := stripAuthorizationOnRedirect(req, []*http.Request{via}); err != nil {
				t.Fatal(err)
			}
			if got := req.Header.Get("Authorization"); got != tc.wantHeader {
				t.Fatalf("Authorization = %q, want %q", got, tc.wantHeader)
			}
		})
	}
}

func TestStripAuthorizationOnRedirectLimitsRedirects(t *testing.T) {
	via := make([]*http.Request, maxAssetRedirects)
	for i := range via {
		via[i], _ = http.NewRequest(http.MethodGet, "https://repo.example/", nil)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://repo.example/", nil)

	if err := stripAuthorizationOnRedirect(req, via); err == nil {
		t.Fatal("expected an error after too many redirects")
	}
}

func TestParseNetrcIgnoresDefault(t *testing.T) {
	creds, logs := parseNetrcForTest(t, strings.Join([]string{
		"machine Example.COM login alice password secret",
		"default login guest password guest-secret",
		"",
	}, "\n"))

	if got := creds["example.com"]; got != (netrcCredentials{"alice", "secret"}) {
		t.Fatalf("got %+v, want alice/secret", got)
	}
	if len(creds) != 1 {
		t.Fatalf("got %d entries, want 1", len(creds))
	}
	if !strings.Contains(logs, ".netrc default entry found; explicitly ignoring default credentials") {
		t.Fatalf("logs = %q, want warning about ignored default credentials", logs)
	}
}

func TestParseNetrcTokensSplitAcrossLines(t *testing.T) {
	creds, _ := parseNetrcForTest(t, "machine repo.example\nlogin alice\npassword secret")

	if got := creds["repo.example"]; got != (netrcCredentials{"alice", "secret"}) {
		t.Fatalf("got %+v, want alice/secret", got)
	}
}

func TestParseNetrcUsesLastDuplicateMachineEntry(t *testing.T) {
	creds, _ := parseNetrcForTest(t, strings.Join([]string{
		"machine repo.example login alice password old",
		"machine repo.example login bob password new",
	}, "\n"))

	if got := creds["repo.example"]; got != (netrcCredentials{"bob", "new"}) {
		t.Fatalf("got %+v, want bob/new", got)
	}
}

func TestParseNetrcIgnoresTokensInsideMacdef(t *testing.T) {
	creds, _ := parseNetrcForTest(t, strings.Join([]string{
		"machine repo.example login alice password secret",
		"macdef init",
		"machine ignored.example login mallory password secret",
		"",
		"machine after.example login carol password secret",
		"",
	}, "\n"))

	if _, ok := creds["ignored.example"]; ok {
		t.Fatal("macdef contents must be ignored")
	}
	if _, ok := creds["after.example"]; !ok {
		t.Fatal("entries after a macdef must be parsed")
	}
}

func TestParseNetrcIgnoresComments(t *testing.T) {
	creds, _ := parseNetrcForTest(t, strings.Join([]string{
		"# machine ignored.example login mallory password secret",
		"  # machine also-ignored.example login mallory password secret",
		"machine repo.example login alice password secret",
	}, "\n"))

	if len(creds) != 1 || creds["repo.example"] != (netrcCredentials{"alice", "secret"}) {
		t.Fatalf("got %+v, want only repo.example", creds)
	}
}

func TestParseNetrcPreservesHashInsidePassword(t *testing.T) {
	creds, _ := parseNetrcForTest(t, "machine repo.example login alice password #secret\n")

	if got := creds["repo.example"]; got != (netrcCredentials{"alice", "#secret"}) {
		t.Fatalf("got %+v, want alice/#secret", got)
	}
}

func TestParseNetrcQuotedTokens(t *testing.T) {
	creds, _ := parseNetrcForTest(t,
		`machine repo.example login "alice smith" password "pa ss\"wo\\rd"`+"\n")

	want := netrcCredentials{"alice smith", `pa ss"wo\rd`}
	if got := creds["repo.example"]; got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseNetrcUnterminatedQuote(t *testing.T) {
	creds, logs := parseNetrcForTest(t, strings.Join([]string{
		`machine broken.example login alice password "oops`,
		"machine repo.example login alice password secret",
	}, "\n"))

	if _, ok := creds["broken.example"]; ok {
		t.Fatal("entry with an unterminated quote must be dropped")
	}
	if _, ok := creds["repo.example"]; !ok {
		t.Fatal("later valid entries must be kept")
	}
	if !strings.Contains(logs, "unterminated quoted string on line 1") {
		t.Fatalf("logs = %q, want unterminated quote warning", logs)
	}
}

func TestParseNetrcLogsMalformedEntriesAndKeepsValidEntries(t *testing.T) {
	creds, logs := parseNetrcForTest(t, strings.Join([]string{
		"machine broken.example login",
		"machine repo.example login alice password secret",
	}, "\n"))

	if got := creds["repo.example"]; got != (netrcCredentials{"alice", "secret"}) {
		t.Fatalf("got %+v, want alice/secret", got)
	}
	if !strings.Contains(logs, `malformed .netrc: missing value for "login" on line 1`) {
		t.Fatalf("logs = %q, want warning about malformed entry", logs)
	}
}

func TestLoadNetrcFileMissing(t *testing.T) {
	_, err := loadNetrcFile(filepath.Join(t.TempDir(), "missing"), testutils.NewSilentLogger())
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want a not-exist error", err)
	}
}

func TestApplyNetrcCredentials(t *testing.T) {
	creds := map[string]netrcCredentials{"repo.example": {"alice", "secret"}}

	tests := []struct {
		name   string
		url    string
		header string
		want   string
	}{
		{"https match", "https://repo.example/f", "", "Basic YWxpY2U6c2VjcmV0"},
		{"https case-insensitive host", "https://REPO.example/f", "", "Basic YWxpY2U6c2VjcmV0"},
		{"http refused", "http://repo.example/f", "", ""},
		{"other host", "https://other.example/f", "", ""},
		{"existing header kept", "https://repo.example/f", "Bearer token", "Bearer token"},
		{"url userinfo kept", "https://bob:manual@repo.example/f", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			applyNetrcCredentials(req, creds)

			if got := req.Header.Get("Authorization"); got != tc.want {
				t.Fatalf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyNetrcCredentialsNilMap(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://repo.example/f", nil)
	applyNetrcCredentials(req, nil)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
}

type recordingLogger struct {
	messages []string
}

func (l *recordingLogger) Printf(format string, v ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf(format, v...))
}

func (l *recordingLogger) joinedMessages() string {
	return strings.Join(l.messages, "\n")
}
