package validate

import (
	"testing"

	pb "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/execution/v2"
)

func TestValidateNilPointers(t *testing.T) {

	// The nil pointers in OutputFileSymlinks etc in TestBadUpdateActionResultRequest
	// get replaced by empty structs somewhere in the protobuf library or bindings.
	// So test those cases specifically here.

	tcs := []struct {
		description  string // What makes the ActionResult invalid.
		actionResult *pb.ActionResult
		expected     error
	}{
		{
			description:  "nil *ActionResult",
			actionResult: nil,
			expected:     errNilActionResult,
		},
		{
			description: "nil *OutputFile",
			actionResult: &pb.ActionResult{
				OutputFiles: []*pb.OutputFile{nil},
			},
			expected: errNilOutputFile,
		},
		{
			description: "nil *OutputSymlink in OutputFileSymlinks",
			actionResult: &pb.ActionResult{
				OutputFileSymlinks: []*pb.OutputSymlink{nil},
			},
			expected: errNilOuputFileSymlink,
		},
		{
			description: "nil *OutputSymlink in OutputSymlinks",
			actionResult: &pb.ActionResult{
				OutputSymlinks: []*pb.OutputSymlink{nil},
			},
			expected: errNilOuputSymlink,
		},
		{
			description: "nil *OutputSymlink in OutputDirectorySymlinks",
			actionResult: &pb.ActionResult{
				OutputDirectorySymlinks: []*pb.OutputSymlink{nil},
			},
			expected: errNilOuputDirSymlink,
		},
	}

	for _, tc := range tcs {
		err := ActionResult(tc.actionResult)
		if err != tc.expected {
			t.Error("invalid ActionResult accepted:", tc.description)
		}
	}
}
