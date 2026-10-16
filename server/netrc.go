package server

import (
	"bufio"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/buchgr/bazel-remote/v2/cache"
)

const maxAssetRedirects = 10

type netrcCredentials struct {
	login    string
	password string
}

// loadNetrcFile parses the .netrc file at path, returning credentials keyed
// by lower-cased machine name.
func loadNetrcFile(path string, logger cache.Logger) (map[string]netrcCredentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return parseNetrc(string(data), logger), nil
}

// applyNetrcCredentials sets basic auth from creds on req, but only for HTTPS
// requests that don't already carry credentials of their own.
func applyNetrcCredentials(req *http.Request, creds map[string]netrcCredentials) {
	if req.URL.Scheme != "https" || req.URL.User != nil || req.Header.Get("Authorization") != "" {
		return
	}

	c, ok := creds[strings.ToLower(req.URL.Hostname())]
	if !ok {
		return
	}

	req.SetBasicAuth(c.login, c.password)
}

// stripAuthorizationOnRedirect is an http.Client.CheckRedirect function which
// only forwards the Authorization header to the exact scheme and host of the
// original request. net/http would also forward it to subdomains.
func stripAuthorizationOnRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxAssetRedirects {
		return errors.New("stopped after too many redirects")
	}

	if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}

	return nil
}

func parseNetrc(data string, logger cache.Logger) map[string]netrcCredentials {
	scanner := bufio.NewScanner(strings.NewReader(data))

	var (
		hosts   = make(map[string]netrcCredentials)
		machine string
		creds   netrcCredentials
		inMacro bool
		lineNum int
	)

	commit := func() {
		if machine == "" || creds.login == "" || creds.password == "" {
			return
		}

		hosts[strings.ToLower(machine)] = creds
		machine = ""
		creds = netrcCredentials{}
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if inMacro {
			if strings.TrimSpace(line) == "" {
				inMacro = false
			}
			continue
		}

		fields, err := netrcFields(line)
		if err != nil {
			logger.Printf("malformed .netrc: %v on line %d", err, lineNum)
		}

		for i := 0; i < len(fields); {
			keyword := fields[i]

			switch keyword {
			case "default":
				logger.Printf(".netrc default entry found; explicitly ignoring default credentials")
				machine = ""
				creds = netrcCredentials{}
				i++
				continue
			case "machine", "login", "password", "account", "macdef":
			default:
				// Unknown keyword, skip it and its value.
				i += 2
				continue
			}

			if i+1 >= len(fields) {
				logger.Printf("malformed .netrc: missing value for %q on line %d", keyword, lineNum)
				break
			}

			value := fields[i+1]
			i += 2

			switch keyword {
			case "machine":
				machine = value
				creds = netrcCredentials{}
			case "login":
				creds.login = value
			case "password":
				creds.password = value
			case "macdef":
				inMacro = true
			}

			commit()
			if inMacro {
				break
			}
		}
	}

	commit()

	if err := scanner.Err(); err != nil {
		logger.Printf("failed to read .netrc credentials: %v", err)
	}

	return hosts
}

// netrcFields splits a .netrc line into whitespace separated tokens. As with
// curl, a token can be wrapped in double quotes to include whitespace, with
// backslash escapes for \", \\, \n, \r and \t. Lines starting with '#' are
// comments. On an error the tokens parsed so far are returned.
func netrcFields(line string) ([]string, error) {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return nil, nil
	}

	var fields []string

	for i := 0; i < len(line); {
		if isNetrcSpace(line[i]) {
			i++
			continue
		}

		if line[i] != '"' {
			start := i
			for i < len(line) && !isNetrcSpace(line[i]) {
				i++
			}
			fields = append(fields, line[start:i])
			continue
		}

		i++ // Opening quote.
		var sb strings.Builder
		closed := false
		for i < len(line) && !closed {
			c := line[i]
			i++

			switch {
			case c == '"':
				closed = true
				continue
			case c == '\\' && i < len(line):
				c = line[i]
				i++
				switch c {
				case 'n':
					c = '\n'
				case 'r':
					c = '\r'
				case 't':
					c = '\t'
				}
			}

			sb.WriteByte(c)
		}

		if !closed {
			return fields, errors.New("unterminated quoted string")
		}

		fields = append(fields, sb.String())
	}

	return fields, nil
}

func isNetrcSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
}
