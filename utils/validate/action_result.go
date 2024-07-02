package validate

import (
	"fmt"
	"strings"

	"github.com/buchgr/bazel-remote/v2/cache/hashing"
	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
)

var (
	errNilActionResult = fmt.Errorf("nil *ActionResult")

	errNegativeDigest = fmt.Errorf("Digest has negative SizeBytes")

	errNilOutputFile = fmt.Errorf("nil output file")
	errEmptyPath     = fmt.Errorf("empty path")
	errNilOutputDir  = fmt.Errorf("nil output directory")

	errNilOuputFileSymlink           = fmt.Errorf("nil *OutputSymlink in OutputFileSymlinks")
	errEmptyOutputFileSymlinksPath   = fmt.Errorf("empty path in OutputFileSymlinks")
	errEmptyOutputFileSymlinksTarget = fmt.Errorf("empty target in OutputFileSymlinks")

	errNilOuputSymlink           = fmt.Errorf("nil *OutputSymlink in OuputSymlinks")
	errEmptyOutputSymlinksPath   = fmt.Errorf("empty path in OutputSymlinks")
	errEmptyOutputSymlinksTarget = fmt.Errorf("empty target in OutputSymlinks")

	errNilOuputDirSymlink           = fmt.Errorf("nil *OutputSymlink in OutputDirectorySymlinks")
	errEmptyOutputDirSymlinksPath   = fmt.Errorf("empty path in OutputDirectorySymlinks")
	errEmptyOutputDirSymlinksTarget = fmt.Errorf("empty target in OutputDirectorySymlinks")
)

// Validate the immediate fields in ar, but don't verify ar's
// dependent blobs.
func ActionResult(ar *pb.ActionResult, hasher hashing.Hasher) error {
	if ar == nil {
		return errNilActionResult
	}

	var err error

	for _, f := range ar.OutputFiles {
		if f == nil {
			return errNilOutputFile
		}
		if f.Path == "" {
			return errEmptyPath
		}
		if strings.HasPrefix(f.Path, "/") {
			return fmt.Errorf("absolute path in output file: %q", f.Path)
		}
		if f.Digest == nil {
			return fmt.Errorf("nil Digest for path %q", f.Path)
		}
		err = maybeNilDigest(f.Digest, hasher) // No need to re-check for nil.
		if err != nil {
			return fmt.Errorf("invalid Digest for path %q: %w", f.Path, err)
		}
	}

	for _, d := range ar.OutputDirectories {
		if d == nil {
			return errNilOutputDir
		}
		if strings.HasPrefix(d.Path, "/") {
			return fmt.Errorf("absolute path in output directory: %q", d.Path)
		}
		if d.TreeDigest == nil {
			return fmt.Errorf("nil tree digest pointer for output directory: %q", d.Path)
		}
		err = maybeNilDigest(d.TreeDigest, hasher) // No need to re-check for nil.
		if err != nil {
			return fmt.Errorf("Invalid TreeDigest for path %q: %w", d.Path, err)
		}
	}

	for _, s := range ar.OutputFileSymlinks {
		if s == nil {
			return errNilOuputFileSymlink
		}
		if s.Path == "" {
			return errEmptyOutputFileSymlinksPath
		}
		if s.Target == "" {
			return errEmptyOutputFileSymlinksTarget
		}
		if strings.HasPrefix(s.Path, "/") {
			return fmt.Errorf("absolute path in output file symlink: %q", s.Path)
		}
	}

	for _, s := range ar.OutputSymlinks {
		if s == nil {
			return errNilOuputSymlink
		}
		if s.Path == "" {
			return errEmptyOutputSymlinksPath
		}
		if s.Target == "" {
			return errEmptyOutputSymlinksTarget
		}
		if strings.HasPrefix(s.Path, "/") {
			return fmt.Errorf("absolute path in output symlink: %q", s.Path)
		}
	}

	for _, s := range ar.OutputDirectorySymlinks {
		if s == nil {
			return errNilOuputDirSymlink
		}
		if s.Path == "" {
			return errEmptyOutputDirSymlinksPath
		}
		if s.Target == "" {
			return errEmptyOutputDirSymlinksTarget
		}
		if strings.HasPrefix(s.Path, "/") {
			return fmt.Errorf("absolute path in output directory symlink: %q", s.Path)
		}
	}

	err = maybeNilDigest(ar.StdoutDigest, hasher)
	if err != nil {
		return fmt.Errorf("invalid StdoutDigest: %w", err)
	}
	err = maybeNilDigest(ar.StderrDigest, hasher)
	if err != nil {
		return fmt.Errorf("invalid StderrDigest: %w", err)
	}

	return nil
}

// Verify that The digest hash and size are valid, if it is non-nil.
func maybeNilDigest(d *pb.Digest, hasher hashing.Hasher) error {
	if d == nil {
		return nil
	}

	if d.SizeBytes < 0 {
		return errNegativeDigest
	}
	if err := hasher.Validate(d.Hash); err != nil {
		return fmt.Errorf("Invalid hash: %q", err.Error())
	}

	return nil
}
