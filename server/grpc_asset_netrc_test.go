package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchgr/bazel-remote/v2/cache/disk"
	asset "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/asset/v1"

	"google.golang.org/grpc/codes"

	testutils "github.com/buchgr/bazel-remote/v2/utils"
)

func TestAssetFetchBlobUsesNetrcCredentials(t *testing.T) {
	blobDir := t.TempDir()
	diskCache, err := disk.New(blobDir, 1024*1024, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	ts := newAuthenticatedTestGetServer("alice", "secret")
	defer ts.srv.Close()

	netrc := fmt.Sprintf("machine %s login alice password secret\n", strings.Split(strings.TrimPrefix(ts.srv.URL, "http://"), ":")[0])
	writeNetrcFile(t, netrc)
	srv := newAssetFetchTestGRPCServer(diskCache)
	srv.netrcCredentials = loadNetrcCredentials(testutils.NewSilentLogger())

	resp, err := srv.FetchBlob(ctx, &asset.FetchBlobRequest{
		Uris: []string{ts.srv.URL + "/" + ts.path},
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Status.GetCode() != int32(codes.OK) {
		t.Fatalf("expected successful fetch, got status %d", resp.Status.GetCode())
	}
}

func TestAssetFetchBlobDoesNotUseNetrcWhenAuthorizationQualifierExists(t *testing.T) {
	blobDir := t.TempDir()
	diskCache, err := disk.New(blobDir, 1024*1024, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	var gotAuthorization string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		if gotAuthorization != "Bearer token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		_, _ = w.Write([]byte("blob"))
	}))
	defer ts.Close()

	netrc := fmt.Sprintf("machine %s login alice password secret\n", strings.Split(strings.TrimPrefix(ts.URL, "http://"), ":")[0])
	writeNetrcFile(t, netrc)
	srv := newAssetFetchTestGRPCServer(diskCache)
	srv.netrcCredentials = loadNetrcCredentials(testutils.NewSilentLogger())

	resp, err := srv.FetchBlob(ctx, &asset.FetchBlobRequest{
		Uris: []string{ts.URL + "/blob"},
		Qualifiers: []*asset.Qualifier{
			{
				Name:  "http_header:authorization",
				Value: "Bearer token",
			},
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

func TestLookupNetrcCredentialsIgnoresDefault(t *testing.T) {
	netrc := strings.Join([]string{
		"machine Example.COM login alice password secret",
		"default login guest password guest-secret",
		"",
	}, "\n")
	writeNetrcFile(t, netrc)

	logger := &recordingLogger{}
	loadedCredentials := loadNetrcCredentials(logger)
	creds := lookupNetrcCredentials("example.com", loadedCredentials)
	if creds == nil {
		t.Fatal("expected credentials for matching host")
	}
	if creds.login != "alice" || creds.password != "secret" {
		t.Fatalf("lookupNetrcCredentials returned %+v, want login/password alice/secret", creds)
	}

	if creds := lookupNetrcCredentials("missing.example", loadedCredentials); creds != nil {
		t.Fatalf("lookupNetrcCredentials returned %+v for missing host, want nil credentials", creds)
	}

	if got := logger.joinedMessages(); !strings.Contains(got, ".netrc default entry found; explicitly ignoring default credentials") {
		t.Fatalf("logger messages = %q, want warning about ignored default credentials", got)
	}
}

func TestLookupNetrcCredentialsParsesTokensSplitAcrossLines(t *testing.T) {
	netrc := strings.Join([]string{
		"machine repo.example",
		"login alice",
		"password secret",
		"",
	}, "\n")
	writeNetrcFile(t, netrc)

	loadedCredentials := loadNetrcCredentials(testutils.NewSilentLogger())
	creds := lookupNetrcCredentials("repo.example", loadedCredentials)
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.login != "alice" || creds.password != "secret" {
		t.Fatalf("lookupNetrcCredentials returned %+v, want login/password alice/secret", creds)
	}
}

func TestLookupNetrcCredentialsKeepsCompleteEntryAtEOF(t *testing.T) {
	netrc := strings.Join([]string{
		"machine repo.example",
		"login alice",
		"password secret",
	}, "\n")
	writeNetrcFile(t, netrc)

	loadedCredentials := loadNetrcCredentials(testutils.NewSilentLogger())
	creds := lookupNetrcCredentials("repo.example", loadedCredentials)
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.login != "alice" || creds.password != "secret" {
		t.Fatalf("lookupNetrcCredentials returned %+v, want login/password alice/secret", creds)
	}
}

func TestLookupNetrcCredentialsUsesLastDuplicateMachineEntry(t *testing.T) {
	netrc := strings.Join([]string{
		"machine repo.example login alice password old",
		"machine repo.example login bob password new",
		"",
	}, "\n")
	writeNetrcFile(t, netrc)

	loadedCredentials := loadNetrcCredentials(testutils.NewSilentLogger())
	creds := lookupNetrcCredentials("repo.example", loadedCredentials)
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.login != "bob" || creds.password != "new" {
		t.Fatalf("lookupNetrcCredentials returned %+v, want login/password bob/new", creds)
	}
}

func TestLookupNetrcCredentialsIgnoresTokensInsideMacdef(t *testing.T) {
	netrc := strings.Join([]string{
		"machine repo.example login alice password secret",
		"macdef init",
		"machine ignored.example login mallory password secret",
		"",
		"default login guest password guest-secret",
		"",
	}, "\n")
	writeNetrcFile(t, netrc)

	loadedCredentials := loadNetrcCredentials(testutils.NewSilentLogger())
	creds := lookupNetrcCredentials("ignored.example", loadedCredentials)
	if creds != nil {
		t.Fatalf("lookupNetrcCredentials returned %+v, want nil credentials", creds)
	}
}

func TestLookupNetrcCredentialsIgnoresComments(t *testing.T) {
	netrc := strings.Join([]string{
		"# machine ignored.example login mallory password secret",
		"  # machine also-ignored.example login mallory password secret",
		"machine repo.example login alice password secret",
		"",
	}, "\n")
	writeNetrcFile(t, netrc)

	loadedCredentials := loadNetrcCredentials(testutils.NewSilentLogger())
	creds := lookupNetrcCredentials("repo.example", loadedCredentials)
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.login != "alice" || creds.password != "secret" {
		t.Fatalf("lookupNetrcCredentials returned %+v, want login/password alice/secret", creds)
	}

	creds = lookupNetrcCredentials("ignored.example", loadedCredentials)
	if creds != nil {
		t.Fatalf("lookupNetrcCredentials returned %+v, want nil credentials", creds)
	}

	creds = lookupNetrcCredentials("also-ignored.example", loadedCredentials)
	if creds != nil {
		t.Fatalf("lookupNetrcCredentials returned %+v, want nil credentials", creds)
	}
}

func TestLookupNetrcCredentialsPreservesHashInsidePassword(t *testing.T) {
	netrc := "machine repo.example login alice password #secret\n"
	writeNetrcFile(t, netrc)

	loadedCredentials := loadNetrcCredentials(testutils.NewSilentLogger())
	creds := lookupNetrcCredentials("repo.example", loadedCredentials)
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.login != "alice" || creds.password != "#secret" {
		t.Fatalf("lookupNetrcCredentials returned %+v, want login/password alice/#secret", creds)
	}
}

func TestLookupNetrcCredentialsLogsMalformedEntriesAndKeepsValidEntries(t *testing.T) {
	netrc := strings.Join([]string{
		"machine broken.example login",
		"machine repo.example login alice password secret",
		"",
	}, "\n")
	writeNetrcFile(t, netrc)

	logger := &recordingLogger{}
	loadedCredentials := loadNetrcCredentials(logger)
	creds := lookupNetrcCredentials("repo.example", loadedCredentials)
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.login != "alice" || creds.password != "secret" {
		t.Fatalf("lookupNetrcCredentials returned %+v, want login/password alice/secret", creds)
	}
	if got := logger.joinedMessages(); !strings.Contains(got, "malformed .netrc: missing login value on line 1") {
		t.Fatalf("logger messages = %q, want warning about malformed entry", got)
	}
}

func TestApplyNetrcCredentialsDoesNotOverrideAuthorizationHeader(t *testing.T) {
	netrc := "machine repo.example login alice password secret\n"
	writeNetrcFile(t, netrc)

	loadedCredentials := loadNetrcCredentials(testutils.NewSilentLogger())
	req, err := http.NewRequest(http.MethodGet, "https://repo.example/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer token")

	applyNetrcCredentials(req, loadedCredentials)

	if got := req.Header.Get("Authorization"); got != "Bearer token" {
		t.Fatalf("Authorization header = %q, want %q", got, "Bearer token")
	}
}

func TestApplyNetrcCredentialsDoesNotOverrideURLUserinfo(t *testing.T) {
	netrc := "machine repo.example login alice password secret\n"
	writeNetrcFile(t, netrc)

	loadedCredentials := loadNetrcCredentials(testutils.NewSilentLogger())
	req, err := http.NewRequest(http.MethodGet, "https://bob:manual@repo.example/file", nil)
	if err != nil {
		t.Fatal(err)
	}

	applyNetrcCredentials(req, loadedCredentials)

	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization header = %q, want empty header", got)
	}
}

func writeNetrcFile(t *testing.T, contents string) string {
	t.Helper()

	netrcFile := filepath.Join(t.TempDir(), ".netrc")
	if err := os.WriteFile(netrcFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("NETRC", netrcFile)

	return netrcFile
}

func newAssetFetchTestGRPCServer(diskCache disk.Cache) *grpcServer {
	return &grpcServer{
		cache:        diskCache,
		accessLogger: testutils.NewSilentLogger(),
		errorLogger:  testutils.NewSilentLogger(),
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
