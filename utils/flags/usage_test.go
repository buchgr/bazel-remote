package flags

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestWrapLine(t *testing.T) {
	t.Parallel()

	tcs := []struct {
		input    string
		offset   int
		wrapAt   int
		padding  string
		expected string
	}{
		{
			input:   "the quick brown fox jumped over the lazy dog",
			offset:  2,
			wrapAt:  10,
			padding: "__",
			expected: `the
__quick
__brown
__fox
__jumped
__over the
__lazy dog`,
		},
		{
			input:    "the quick brown fox jumped over the lazy dog",
			wrapAt:   50,
			padding:  "__",
			expected: "the quick brown fox jumped over the lazy dog",
		},
	}

	for _, tc := range tcs {
		result := wrapLine(tc.input, tc.wrapAt, tc.padding)
		if result != tc.expected {
			t.Errorf("Got %q, expected %q", result, tc.expected)
		}
	}
}

func TestWrap(t *testing.T) {
	t.Parallel()

	tcs := []struct {
		input    string
		offset   int
		wrapAt   int
		expected string
	}{
		{
			input: `the quick brown fox jumped over the lazy dog
the second line is even longer than the first, with some super important
information that overflows
and finally a fourth line with some gibberish`,
			offset: 2,
			wrapAt: 25,
			expected: `the quick brown fox
  jumped over the lazy
  dog
  the second line is even
  longer than the first,
  with some super
  important
  information that
  overflows
  and finally a fourth
  line with some
  gibberish`,
		},
	}

	for _, tc := range tcs {
		result := wrap(tc.input, tc.offset, tc.wrapAt)
		if result != tc.expected {
			t.Errorf("Got %q, expected %q", result, tc.expected)
		}
	}
}

func TestHelpPrinter(t *testing.T) {

	// Setting an environment variable in a test is not great, but we
	// don't have a better way to unit test this at the moment.
	os.Setenv("COLUMNS", "35")
	defer os.Unsetenv("COLUMNS")

	flags := []cli.Flag{
		&cli.StringFlag{
			Name:    "foo",
			Value:   "42",
			Usage:   "you really should specify this value, otherwise some terrible things will happen",
			EnvVars: []string{"FOO"},
		},
		&cli.IntFlag{
			Name:    "bar",
			Value:   1,
			Usage:   "this is another flag with a description long enough to test the wrapping",
			EnvVars: []string{"BAR"},
		},
	}

	expected := `bazel-remote - A remote build cache for Bazel and other REAPI clients

USAGE:
   cli.test [options]

OPTIONS:
   --foo value you really should
      specify this value, otherwise
      some terrible things will
      happen (default: "42") [$FOO]

   --bar value this is another
      flag with a description long
      enough to test the wrapping
      (default: 1) [$BAR]

   --help, -h show help
      (default: false)
`

	output := new(bytes.Buffer)

	app := &cli.App{
		Name:   "cli.test",
		Writer: output,
		Flags:  flags,
	}

	// Reset HelpPrinter after this test.
	defer func(old func(w io.Writer, templ string, data interface{}, customFunc map[string]interface{})) {
		cli.HelpPrinterCustom = old
	}(cli.HelpPrinterCustom)

	cli.HelpPrinterCustom = HelpPrinter
	cli.AppHelpTemplate = Template

	// Force the use of cli.HelpPrinterCustom.
	app.ExtraInfo = func() map[string]string { return map[string]string{} }

	app.Action = func(c *cli.Context) error { return nil }
	err := app.Run([]string{"appName", "-h"})
	if err != nil {
		t.Fatal(err)
	}

	if string(output.Bytes()) != expected {
		t.Fatalf("Expected:\n%s\n\nGot:\n%s\n", expected,
			string(output.Bytes()))
	}
}
