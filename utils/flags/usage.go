package flags

import (
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"
	"text/template"
)

const (
	// An arbitrarily large width, that we never expect to hit
	// in practice, which satisfies our internal API.
	defaultWidth = 10000

	// Don't attempt to wrap any more narrow than this.
	minimumWidth = 30
)

// Template describes the help text format.
var Template = `bazel-remote - A remote build cache for Bazel and other REAPI clients

USAGE:
   {{.Name}} [options]

OPTIONS:
   {{range $index, $option := .VisibleFlags}}{{if $index}}
   {{end}}{{wrap $option.String 6}}
{{end}}`

// HelpPrinter writes our custom-formatted help text to `out`.
func HelpPrinter(out io.Writer, templ string, data interface{}, customFuncs map[string]interface{}) {
	maxLineLength := getConsoleWidth()

	funcMap := template.FuncMap{
		"wrap": func(input string, offset int) string {
			return wrap(input, offset, maxLineLength)
		},
	}

	w := tabwriter.NewWriter(out, 1, 8, 2, ' ', 0)
	t := template.Must(template.New("help").Funcs(funcMap).Parse(templ))

	err := t.Execute(w, data)
	if err != nil {
		log.Fatalf("Failed to apply the template or write the results: %q", err)
	}
	err = w.Flush()
	if err != nil {
		log.Fatalf("Failed to flush help text: %q", err)
	}
}

// Wrap a possibly multiline input string at word boundaries when it
// reaches `wrapAt`, and prefix wrapped lines with `offset` spaces.
func wrap(input string, offset int, wrapAt int) string {
	var sb strings.Builder

	lines := strings.Split(input, "\n")

	prefix := strings.Repeat(" ", offset)

	for i, line := range lines {
		if i != 0 {
			sb.WriteString(prefix)
		}

		sb.WriteString(wrapLine(line, wrapAt, prefix))

		if i != len(lines)-1 {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// Wrap a single line input string at word boundaries, once it reaches
// `wrapAt`, with `prefix` added to the start of wrapped lines.
// Note that this does not preserve whitespace exactly.
func wrapLine(input string, wrapAt int, padding string) string {

	offset := len(padding)

	if wrapAt <= offset {
		// We can't meaningfully wrap the input, return it unchanged.
		return input
	}

	if len(input) <= wrapAt-offset {
		// Simple case- the input is small enough to return unchanged.
		return input
	}

	targetWidth := wrapAt - offset
	words := strings.Fields(input)
	if len(words) == 0 {
		// Input must be empty or all whitespace, return the input unchanged.
		return input
	}

	// Place at least one word on the first line.
	wrapped := words[0]
	spaceLeft := targetWidth - len(wrapped)

	for _, word := range words[1:] {
		if len(word)+1 > spaceLeft {
			// Not enough room for the word, start a new line with it.
			wrapped += "\n" + padding + word
			spaceLeft = targetWidth - len(word)
		} else {
			// There's room on the current line for this word.
			wrapped += " " + word
			spaceLeft -= 1 + len(word)
		}
	}

	return wrapped
}

func getConsoleWidth() int {

	// If the COLUMNS environment variable is set, try to use it.
	columns := os.Getenv("COLUMNS")
	if columns != "" {
		width, err := strconv.Atoi(strings.TrimSpace(string(columns)))
		if err == nil {

			if width < minimumWidth {
				return minimumWidth
			}

			return width
		}
	}

	// Otherwise query the terminal for the width.
	// This seems to work on both linux and mac.
	cmd := exec.Command("tput", "cols")
	cmd.Stdin = os.Stdin
	output, err := cmd.Output()
	if err != nil {
		return defaultWidth
	}

	width, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return defaultWidth
	}
	if width < minimumWidth {
		return minimumWidth
	}

	return width
}
