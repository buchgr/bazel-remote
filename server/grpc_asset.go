package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	grpc_status "google.golang.org/grpc/status"

	asset "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/asset/v1"
	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"

	"github.com/buchgr/bazel-remote/v2/cache"
)

// FetchServer implementation

var errNilFetchBlobRequest = grpc_status.Error(codes.InvalidArgument,
	"expected a non-nil *FetchBlobRequest")

var resourceExhaustedResponse = asset.FetchBlobResponse{
	Status: &status.Status{
		Code:    int32(codes.ResourceExhausted),
		Message: "Storage appears to be falling behind",
	},
}

func (s *grpcServer) FetchBlob(ctx context.Context, req *asset.FetchBlobRequest) (*asset.FetchBlobResponse, error) {

	var sha256Str string

	// Q: which combinations of qualifiers to support?
	// * simple file, identified by sha256 SRI AND/OR recognisable URL
	// * git repository, identified by ???
	// * go repository, identified by tag/branch/???

	// "strong" identifiers:
	// checksum.sri -> direct lookup for sha256 (easy), indirect lookup for
	//     others (eg sha256 of the SRI hash).
	// vcs.commit + .git extension -> indirect lookup? or sha1 lookup?
	//     But this could waste a lot of space.
	//
	// "weak" identifiers:
	// vcs.branch + .git extension -> indirect lookup, with timeout check
	//    directory: limit one of the vcs.* returns
	//               insert to tree into the CAS?
	//
	//    git archive --format=tar --remote=http://foo/bar.git ref dir...

	// For TTL items, we need another (persistent) index, eg BadgerDB?
	// key -> CAS sha256 + timestamp
	// Should we place a limit on the size of the index?

	if req == nil {
		return nil, errNilFetchBlobRequest
	}

	globalHeader := http.Header{}

	uriSpecificHeaders := make(map[int]http.Header)

	for _, q := range req.GetQualifiers() {
		if q == nil {
			return &asset.FetchBlobResponse{
				Status: &status.Status{
					Code:    int32(codes.InvalidArgument),
					Message: "unexpected nil qualifier in FetchBlobRequest",
				},
			}, nil
		}

		const QualifierHTTPHeaderPrefix = "http_header:"
		const QualifierHTTPHeaderUrlPrefix = "http_header_url:"

		if strings.HasPrefix(q.Name, QualifierHTTPHeaderPrefix) {
			key := q.Name[len(QualifierHTTPHeaderPrefix):]

			globalHeader[key] = strings.Split(q.Value, ",")
			continue
		} else if strings.HasPrefix(q.Name, QualifierHTTPHeaderUrlPrefix) {
			idxAndKey := q.Name[len(QualifierHTTPHeaderUrlPrefix):]
			parts := strings.Split(idxAndKey, ":")
			if len(parts) != 2 {
				s.errorLogger.Printf("invalid http_header_url qualifier: \"%s\"", idxAndKey)
				continue
			}

			uriIndex, err := strconv.Atoi(parts[0])
			if err != nil {
				s.errorLogger.Printf("failed to parse URI index as int: %s", err)
				continue
			}

			if uriIndex < 0 || uriIndex >= len(req.GetUris()) {
				s.errorLogger.Printf("URI index for header is out of range [0 - %d]: %d", len(req.GetUris())-1, uriIndex)
				continue
			}

			if _, found := uriSpecificHeaders[uriIndex]; !found {
				uriSpecificHeaders[uriIndex] = make(http.Header)
			}
			uriSpecificHeaders[uriIndex].Add(parts[1], q.Value)

			continue
		}

		if q.Name == "checksum.sri" && strings.HasPrefix(q.Value, "sha256-") {
			// Ref: https://developer.mozilla.org/en-US/docs/Web/Security/Subresource_Integrity

			b64hash := strings.TrimPrefix(q.Value, "sha256-")

			decoded, err := base64.StdEncoding.DecodeString(b64hash)
			if err != nil {
				s.errorLogger.Printf("failed to base64 decode \"%s\": %v",
					b64hash, err)
				continue
			}

			sha256Str = hex.EncodeToString(decoded)

			found, size := s.cache.Contains(ctx, cache.CAS, sha256Str, -1)
			if !found {
				continue
			}

			if size < 0 {
				// We don't know the size yet (bad http backend?).
				r, actualSize, err := s.cache.Get(ctx, cache.CAS, sha256Str, -1, 0)
				if r != nil {
					defer func() { _ = r.Close() }()
				}
				if err != nil || actualSize < 0 {
					s.errorLogger.Printf("failed to get CAS %s from proxy backend size: %d err: %v",
						sha256Str, actualSize, err)
					continue
				}
				size = actualSize
			}

			return &asset.FetchBlobResponse{
				Status: &status.Status{Code: int32(codes.OK)},
				BlobDigest: &pb.Digest{
					Hash:      sha256Str,
					SizeBytes: size,
				},
			}, nil
		}
	}

	// Cache miss.

	// See if we can download one of the URIs.

	for uriIndex, uri := range req.GetUris() {
		uriSpecificHeader := globalHeader.Clone()
		if header, found := uriSpecificHeaders[uriIndex]; found {
			for key, value := range header {
				uriSpecificHeader[key] = value
			}
		}

		actualHash, size, err := s.fetchItem(ctx, uri, uriSpecificHeader, sha256Str)
		if err == nil {
			return &asset.FetchBlobResponse{
				Status: &status.Status{Code: int32(codes.OK)},
				BlobDigest: &pb.Digest{
					Hash:      actualHash,
					SizeBytes: size,
				},
				Uri: uri,
			}, nil
		}

		if translateGRPCErrCodeFromClient(err) == codes.ResourceExhausted {
			return &resourceExhaustedResponse, nil
		}

		// Not a simple file. Not yet handled...
	}

	return &asset.FetchBlobResponse{
		Status: &status.Status{Code: int32(codes.NotFound)},
	}, nil
}

func (s *grpcServer) fetchItem(ctx context.Context, uri string, headers http.Header, expectedHash string) (string, int64, error) {
	u, err := url.Parse(uri)
	if err != nil {
		s.errorLogger.Printf("unable to parse URI: %s err: %v", uri, err)
		return "", int64(-1), err
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		s.errorLogger.Printf("unsupported URI: %s", uri)
		return "", int64(-1), fmt.Errorf("Unknown URL scheme: %q", u.Scheme)
	}

	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		s.errorLogger.Printf("failed to create http.Request: %s err: %v", uri, err)
		return "", int64(-1), err
	}

	req.Header = headers

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.errorLogger.Printf("failed to get URI: %s err: %v", uri, err)
		return "", int64(-1), err
	}
	defer func() { _ = resp.Body.Close() }()
	rc := resp.Body

	s.accessLogger.Printf("GRPC ASSET FETCH %s %s", uri, resp.Status)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", int64(-1), fmt.Errorf("Unsuccessful HTTP status code: %d", resp.StatusCode)
	}

	expectedSize := resp.ContentLength
	if expectedHash == "" || expectedSize < 0 {
		// We can't call Put until we know the hash and size.

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			s.errorLogger.Printf("failed to read data: %v", uri)
			return "", int64(-1), err
		}

		expectedSize = int64(len(data))
		hashBytes := sha256.Sum256(data)
		hashStr := hex.EncodeToString(hashBytes[:])

		if expectedHash != "" && hashStr != expectedHash {
			s.errorLogger.Printf("URI data has hash %s, expected %s",
				hashStr, expectedHash)
			return "", int64(-1), fmt.Errorf("URI data has hash %s, expected %s", hashStr, expectedHash)
		}

		expectedHash = hashStr
		rc = io.NopCloser(bytes.NewReader(data))
	}

	err = s.cache.Put(ctx, cache.CAS, expectedHash, expectedSize, rc)
	if err != nil && err != io.EOF {
		s.errorLogger.Printf("failed to Put %s: %v", expectedHash, err)
		return "", int64(-1), err
	}

	return expectedHash, expectedSize, nil
}

func (s *grpcServer) FetchDirectory(context.Context, *asset.FetchDirectoryRequest) (*asset.FetchDirectoryResponse, error) {
	return nil, nil
}

/* PushServer implementation
func (s *grpcServer) PushBlob(context.Context, *asset.PushBlobRequest) (*asset.PushBlobResponse, error) {
	return nil, nil
}

func (s *grpcServer) PushDirectory(context.Context, *asset.PushDirectoryRequest) (*asset.PushDirectoryResponse, error) {
	return nil, nil
}
*/
