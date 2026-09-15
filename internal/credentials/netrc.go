package credentials

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

const maxNetrcBytes = 4 << 20 // 4 MiB

type netrcEntry struct {
	machine  string
	login    string
	password string
	account  string
}

type netrcStore struct {
	path  string
	notes []string
}

func newNetrcStore() *netrcStore {
	return &netrcStore{path: netrcPath()}
}

func netrcPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home + "/.netrc"
}

func (n *netrcStore) read(host string) (map[string]string, bool) {
	n.notes = nil
	entries, err := n.parse()
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		if e.machine == host {
			return n.toMap(e), true
		}
	}
	// Fallback to default.
	for _, e := range entries {
		if e.machine == "default" {
			return n.toMap(e), true
		}
	}
	return nil, false
}

func (n *netrcStore) toMap(e netrcEntry) map[string]string {
	m := map[string]string{}
	if e.password != "" {
		m["api_key"] = e.password
	}
	if e.account != "" {
		m["account_id"] = e.account
	} else if e.login != "" {
		m["account_id"] = e.login
	}
	return m
}

func (n *netrcStore) parse() ([]netrcEntry, error) {
	if n.path == "" {
		return nil, nil
	}
	info, err := os.Stat(n.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Size() > maxNetrcBytes {
		return nil, fmt.Errorf("netrc too large")
	}
	if info.Mode().Perm()&0o077 != 0 {
		n.notes = append(n.notes, fmt.Sprintf("~/.netrc mode is %04o (world-readable)", info.Mode().Perm()))
	}

	f, err := os.Open(n.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tokens := tokenize(f)
	var entries []netrcEntry
	var cur *netrcEntry

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch tok {
		case "machine":
			if cur != nil {
				entries = append(entries, *cur)
			}
			cur = &netrcEntry{}
			if i+1 < len(tokens) {
				i++
				cur.machine = tokens[i]
			}
		case "default":
			if cur != nil {
				entries = append(entries, *cur)
			}
			cur = &netrcEntry{machine: "default"}
		case "macdef":
			// Skip macro name and body until next blank line.
			if i+1 < len(tokens) {
				i++ // skip macro name
			}
			for i+1 < len(tokens) && tokens[i+1] != "" {
				i++
			}
		case "login":
			if cur != nil && i+1 < len(tokens) {
				i++
				cur.login = tokens[i]
			}
		case "password":
			if cur != nil && i+1 < len(tokens) {
				i++
				cur.password = tokens[i]
			}
		case "account":
			if cur != nil && i+1 < len(tokens) {
				i++
				cur.account = tokens[i]
			}
		}
	}
	if cur != nil {
		entries = append(entries, *cur)
	}
	return entries, nil
}

func tokenize(r *os.File) []string {
	var tokens []string
	scan := bufio.NewScanner(r)
	for scan.Scan() {
		line := scan.Text()
		fields := strings.Fields(line)
		if len(fields) == 0 {
			// blank line -> sentinel
			tokens = append(tokens, "")
			continue
		}
		tokens = append(tokens, fields...)
	}
	return tokens
}

func (n *netrcStore) exists() bool {
	if n.path == "" {
		return false
	}
	_, err := os.Stat(n.path)
	return err == nil
}

func (n *netrcStore) write() error {
	return errors.New("netrc is read-only")
}

func (n *netrcStore) delete() error {
	return errors.New("netrc is read-only")
}
