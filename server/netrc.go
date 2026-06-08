package server

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/buchgr/bazel-remote/v2/cache"
)

type netrcCredentials struct {
	login    string
	password string
}

type netrcEntry struct {
	machine  string
	login    string
	password string
}

type netrcFileCredentials struct {
	hosts map[string]netrcCredentials
}

func applyNetrcCredentials(req *http.Request, loadedCredentials netrcFileCredentials) {
	if req.URL == nil {
		return
	}

	if hasAuthorizationHeader(req.Header) {
		return
	}

	if req.URL.User != nil {
		return
	}

	creds := lookupNetrcCredentials(req.URL.Hostname(), loadedCredentials)
	if creds == nil {
		return
	}

	req.SetBasicAuth(creds.login, creds.password)
}

func hasAuthorizationHeader(header http.Header) bool {
	for key, values := range header {
		if !strings.EqualFold(key, "Authorization") {
			continue
		}

		for _, value := range values {
			if value != "" {
				return true
			}
		}
	}

	return false
}

func loadNetrcCredentials(logger cache.Logger) netrcFileCredentials {
	path, err := netrcPath()
	if err != nil {
		if logger != nil {
			logger.Printf("failed to read .netrc credentials: %v", err)
		}
		return emptyNetrcFileCredentials()
	}
	if path == "" {
		return emptyNetrcFileCredentials()
	}

	return loadNetrcCredentialsForPath(path, logger)
}

func lookupNetrcCredentials(host string, credsByPath netrcFileCredentials) *netrcCredentials {
	return credsByPath.lookup(host)
}

func loadNetrcCredentialsForPath(path string, logger cache.Logger) netrcFileCredentials {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return emptyNetrcFileCredentials()
		}
		if logger != nil {
			logger.Printf("failed to read .netrc credentials: %v", err)
		}
		return emptyNetrcFileCredentials()
	}

	entries := parseNetrcEntries(string(data), logger)

	return newNetrcFileCredentials(entries)
}

func netrcPath() (string, error) {
	if path := os.Getenv("NETRC"); path != "" {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", nil
	}

	return filepath.Join(home, ".netrc"), nil
}

func parseNetrcEntries(data string, logger cache.Logger) []netrcEntry {
	scanner := bufio.NewScanner(strings.NewReader(data))

	var (
		entries []netrcEntry
		entry   netrcEntry
		inMacro bool
		lineNum int
	)

	commitEntry := func() {
		if entry.machine == "" || entry.login == "" || entry.password == "" {
			return
		}

		entries = append(entries, entry)
		entry = netrcEntry{}
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

		fields := netrcFields(line)
		for i := 0; i < len(fields); {
			switch fields[i] {
			case "machine":
				if i+1 >= len(fields) {
					logMalformedNetrc(logger, "missing machine name", lineNum)
					i++
					continue
				}

				entry = netrcEntry{machine: fields[i+1]}
				i += 2
			case "default":
				if logger != nil {
					logger.Printf(".netrc default entry found; explicitly ignoring default credentials")
				}
				entry = netrcEntry{}
				i++
			case "login":
				if i+1 >= len(fields) {
					logMalformedNetrc(logger, "missing login value", lineNum)
					i++
					continue
				}

				entry.login = fields[i+1]
				i += 2
			case "password":
				if i+1 >= len(fields) {
					logMalformedNetrc(logger, "missing password value", lineNum)
					i++
					continue
				}

				entry.password = fields[i+1]
				i += 2
			case "account":
				if i+1 >= len(fields) {
					logMalformedNetrc(logger, "missing account value", lineNum)
					i++
					continue
				}

				i += 2
			case "macdef":
				if i+1 >= len(fields) {
					logMalformedNetrc(logger, "missing macro name", lineNum)
					i++
					continue
				}

				i += 2
				inMacro = true
			default:
				if i+1 < len(fields) {
					i += 2
				} else {
					i++
				}
			}

			commitEntry()
			if inMacro {
				break
			}
		}
	}

	commitEntry()

	if err := scanner.Err(); err != nil {
		if logger != nil {
			logger.Printf("failed to read .netrc credentials: %v", err)
		}
	}

	return entries
}

func emptyNetrcFileCredentials() netrcFileCredentials {
	return netrcFileCredentials{hosts: make(map[string]netrcCredentials)}
}

func netrcFields(line string) []string {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return nil
	}

	return strings.Fields(line)
}

func logMalformedNetrc(logger cache.Logger, msg string, lineNum int) {
	if logger != nil {
		logger.Printf("malformed .netrc: %s on line %d", msg, lineNum)
	}
}

func newNetrcFileCredentials(entries []netrcEntry) netrcFileCredentials {
	credsByHost := netrcFileCredentials{
		hosts: make(map[string]netrcCredentials, len(entries)),
	}

	for _, entry := range entries {
		creds := netrcCredentials{
			login:    entry.login,
			password: entry.password,
		}

		key := strings.ToLower(entry.machine)
		credsByHost.hosts[key] = creds
	}

	return credsByHost
}

func (c netrcFileCredentials) lookup(host string) *netrcCredentials {
	if creds, ok := c.hosts[strings.ToLower(host)]; ok {
		hostCreds := creds
		return &hostCreds
	}

	return nil
}
