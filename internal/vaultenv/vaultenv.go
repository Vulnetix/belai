// Package vaultenv holds the environment variables a Pix sandbox's vault grants it,
// puts them into the commands the agent runs, and scrubs them back out of
// everything the sandbox writes.
//
// The values come from the organisation's secrets vault (vdb-site
// handler/vault_handlers.go VaultHostEnv) over a lease the session process keeps
// fresh (internal/sessionsync/vaultenv.go). They live here, in memory, and nowhere
// else: never in settings.json, never in this process's own environment, never in a
// file. A tool call gets them in its own environment, set after proc.ScrubbedEnv
// has removed the harness's credentials, so the process a command starts holds
// them and Belai's does not.
//
// What leaves a command passes Scrub first: the tool result the model sees, the
// session transcript (JSONL), a background process's log, and Belai's own log.
// Scrub matches the value and the forms it takes in output (base64, hex, URL and JSON
// escapes, a shell-quoted form) and writes [vault:NAME] in its place. A value that is
// split across two writes is still caught by Writer, which holds back the tail.
//
// Scrubbing hides a value in what Belai records. It cannot stop a command that
// transforms a value (a hash, a reversal, a split) or sends it somewhere, so the
// vault pairs it with deny-by-default egress for sandboxes that hold secrets.
package vaultenv

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// MinLen is the shortest value that is injected. A shorter one cannot be scrubbed
// without hiding ordinary words, so the vault refuses to store it too.
const MinLen = 8

// MaxVars bounds what one lease may carry.
const MaxVars = 128

// Var is one environment variable.
type Var struct {
	Name  string
	Value string

	// ExpiresAt is when the vault stops granting it; zero is never. It is enforced
	// here on every call, so an expired variable is not injected even before the
	// next lease refresh.
	ExpiresAt time.Time
}

// String never shows the value.
func (v Var) String() string { return v.Name + "=[hidden]" }

// Store is the process's set of vault variables.
type Store struct {
	mu         sync.RWMutex
	active     []Var
	leaseUntil time.Time

	// known is every value seen in this process, by name, kept so output is still
	// scrubbed after a variable expires or is revoked.
	known    map[string]string
	scrubber *Scrubber
	hits     map[string]int
}

// Default is the store the harness uses.
var Default = &Store{}

// Replace installs a new lease: the variables now granted, valid until leaseUntil.
// Values the vault no longer grants stop being injected at once; they stay known to
// the scrubber.
func (s *Store) Replace(vars []Var, leaseUntil time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(vars) > MaxVars {
		vars = vars[:MaxVars]
	}
	if s.known == nil {
		s.known = map[string]string{}
	}
	out := make([]Var, 0, len(vars))
	for _, v := range vars {
		if v.Name == "" || len(v.Value) < MinLen {
			continue
		}
		out = append(out, v)
		if s.known[v.Name] != v.Value {
			s.known[v.Name] = v.Value
			s.scrubber = nil
		}
	}
	s.active = out
	s.leaseUntil = leaseUntil
}

// live is the variables that may be injected at now.
func (s *Store) live(now time.Time) []Var {
	if !s.leaseUntil.IsZero() && now.After(s.leaseUntil) {
		return nil
	}
	var out []Var
	for _, v := range s.active {
		if v.ExpiresAt.IsZero() || now.Before(v.ExpiresAt) {
			out = append(out, v)
		}
	}
	return out
}

// Environ is NAME=VALUE for each variable a command may have at now.
func (s *Store) Environ(now time.Time) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	live := s.live(now)
	out := make([]string, 0, len(live))
	for _, v := range live {
		out = append(out, v.Name+"="+v.Value)
	}
	return out
}

// Names lists the variables a command has at now, sorted. It carries no value.
func (s *Store) Names(now time.Time) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	live := s.live(now)
	out := make([]string, 0, len(live))
	for _, v := range live {
		out = append(out, v.Name)
	}
	sort.Strings(out)
	return out
}

// HasActive reports whether any variable would be injected at now.
func (s *Store) HasActive(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.live(now)) > 0
}

// Hits returns, and resets, how many times each variable was scrubbed from output.
func (s *Store) Hits() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hits
	s.hits = nil
	return h
}

// Restore adds counts back after they could not be reported.
func (s *Store) Restore(hits map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hits == nil {
		s.hits = map[string]int{}
	}
	for name, n := range hits {
		s.hits[name] += n
	}
}

func (s *Store) current() *Scrubber {
	s.mu.RLock()
	sc := s.scrubber
	s.mu.RUnlock()
	if sc != nil {
		return sc
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scrubber == nil {
		s.scrubber = newScrubber(s.known)
	}
	return s.scrubber
}

func (s *Store) record(name string, n int) {
	s.mu.Lock()
	if s.hits == nil {
		s.hits = map[string]int{}
	}
	s.hits[name] += n
	s.mu.Unlock()
}

// Scrub replaces every vault value in text with [vault:NAME].
func (s *Store) Scrub(text string) string {
	sc := s.current()
	if sc.empty() {
		return text
	}
	out, counts := sc.scrub(text)
	for name, n := range counts {
		s.record(name, n)
	}
	return out
}

// ScrubBytes is Scrub over bytes.
func (s *Store) ScrubBytes(b []byte) []byte {
	sc := s.current()
	if sc.empty() {
		return b
	}
	out, counts := sc.scrub(string(b))
	for name, n := range counts {
		s.record(name, n)
	}
	return []byte(out)
}

// Scrubber replaces known values and their encoded forms.
type Scrubber struct {
	forms  []form
	maxLen int
}

type form struct {
	text string
	name string
}

func newScrubber(known map[string]string) *Scrubber {
	var forms []form
	seen := map[string]bool{}
	add := func(name, text string) {
		if len(text) < MinLen || seen[text] {
			return
		}
		seen[text] = true
		forms = append(forms, form{text: text, name: name})
	}
	for name, value := range known {
		add(name, value)
		// A multi-line value (a private key) shows up line by line.
		if strings.ContainsAny(value, "\r\n") {
			for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' }) {
				add(name, strings.TrimSpace(line))
			}
		}
		b := []byte(value)
		add(name, base64.StdEncoding.EncodeToString(b))
		add(name, base64.RawStdEncoding.EncodeToString(b))
		add(name, base64.URLEncoding.EncodeToString(b))
		add(name, base64.RawURLEncoding.EncodeToString(b))
		add(name, hex.EncodeToString(b))
		add(name, strings.ToUpper(hex.EncodeToString(b)))
		add(name, url.QueryEscape(value))
		add(name, url.PathEscape(value))
		if q, err := json.Marshal(value); err == nil && len(q) > 2 {
			add(name, string(q[1:len(q)-1]))
		}
		// The same without encoding/json's HTML escaping (<, >, & as \u00XX), which
		// other encoders do not apply.
		var raw bytes.Buffer
		enc := json.NewEncoder(&raw)
		enc.SetEscapeHTML(false)
		if enc.Encode(value) == nil {
			if q := strings.TrimSuffix(raw.String(), "\n"); len(q) > 2 {
				add(name, q[1:len(q)-1])
			}
		}
		if strings.Contains(value, "'") {
			add(name, strings.ReplaceAll(value, "'", `'\''`))
		}
	}
	// Longest first, so a value that contains another is replaced whole.
	sort.Slice(forms, func(i, j int) bool {
		if len(forms[i].text) != len(forms[j].text) {
			return len(forms[i].text) > len(forms[j].text)
		}
		return forms[i].text < forms[j].text
	})
	max := 0
	for _, f := range forms {
		if len(f.text) > max {
			max = len(f.text)
		}
	}
	return &Scrubber{forms: forms, maxLen: max}
}

func (sc *Scrubber) empty() bool { return sc == nil || len(sc.forms) == 0 }

func placeholder(name string) string { return "[vault:" + name + "]" }

func (sc *Scrubber) scrub(text string) (string, map[string]int) {
	var counts map[string]int
	for _, f := range sc.forms {
		if n := strings.Count(text, f.text); n > 0 {
			text = strings.ReplaceAll(text, f.text, placeholder(f.name))
			if counts == nil {
				counts = map[string]int{}
			}
			counts[f.name] += n
		}
	}
	return text, counts
}

// Writer scrubs a stream. A value split across two writes is caught because the
// last maxLen-1 bytes are held back until more arrives or Close is called.
type Writer struct {
	mu    sync.Mutex
	w     io.Writer
	st    *Store
	buf   []byte
	maxLn int
}

// NewWriter wraps w so what is written to it is scrubbed against st.
func NewWriter(w io.Writer, st *Store) *Writer { return &Writer{w: w, st: st} }

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sc := w.st.current()
	if sc.empty() {
		// Nothing to hide now; anything held back from before still goes through.
		if len(w.buf) == 0 {
			return w.w.Write(p)
		}
	}
	w.buf = append(w.buf, p...)
	text := string(w.buf)
	if !sc.empty() {
		var counts map[string]int
		text, counts = sc.scrub(text)
		for name, n := range counts {
			w.st.record(name, n)
		}
	}
	hold := 0
	if !sc.empty() {
		hold = sc.maxLen - 1
	}
	if hold > len(text) {
		hold = len(text)
	}
	emit := text[:len(text)-hold]
	w.buf = append(w.buf[:0], text[len(text)-hold:]...)
	if emit != "" {
		if _, err := io.WriteString(w.w, emit); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Close writes what is held back.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) == 0 {
		return nil
	}
	text := w.st.Scrub(string(w.buf))
	w.buf = nil
	_, err := io.WriteString(w.w, text)
	return err
}
