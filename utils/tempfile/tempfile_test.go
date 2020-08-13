package tempfile_test

import (
	"io/ioutil"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/buchgr/bazel-remote/utils/tempfile"
)

func TestTempfileCreator(t *testing.T) {
	tfc := tempfile.NewCreator()

	dir, err := ioutil.TempDir("", "foo")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	targetFile := path.Join(dir, "foo")
	tf, err := tfc.Create(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tf.Name())

	expectedPrefix := targetFile + "."
	if !strings.HasPrefix(tf.Name(), expectedPrefix) {
		t.Fatalf("Expected tempfile \"%s\" to have prefix \"%s\"",
			tf.Name(), expectedPrefix)
	}
}
