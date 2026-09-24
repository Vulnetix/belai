package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2/lexers"
)

const (
	// readDefaultLines is how many lines a Read returns when no limit is
	// given — the figure models trained on this tool expect.
	readDefaultLines = 2000
	// readMaxLineBytes clips a single over-long line (minified JS, a JSON
	// blob) so one line cannot spend the whole byte budget.
	readMaxLineBytes = 2000
)

// Read is the file-read tool.
//
// For the model it follows the trained contract: offset is a 1-based line
// number, limit a line count, and the output is `cat -n` formatted — each line
// prefixed with its right-aligned number and a tab. The number is coordinates,
// not content, so Grep's `path:line:text` can be handed straight to offset.
type Read struct {
	Cwd      *Cwd
	Root     string
	MaxBytes int64
	// Verbatim returns the file's bytes exactly as they are — no gutter, no
	// paging trailer, offset and limit ignored — for harness callers such as
	// @file attachments, whose content is diffed against the index.
	Verbatim bool
	// Reads, when set, records each successful read so Edit and Write can
	// tell a file the model has seen from one it has not; see ReadState.
	Reads *ReadState
}

// Definition returns the static tool metadata.
func (r *Read) Definition() Definition {
	return Definition{
		Name: "Read",
		Description: "Read a text file under the working directory. " +
			"Output is `cat -n` formatted: every line is prefixed with its 1-based line number and a tab. That prefix is not part of the file — never include it in Edit's old_string. " +
			"By default up to 2000 lines are returned from the start of the file, lines longer than 2000 bytes are clipped, and the whole result is bounded (64 KiB by default). " +
			"A partial read ends with a `[Read: lines A–B of N; …]` trailer that says whether you reached the end of the file or which offset to continue from; a whole-file read has no trailer. Do not re-read a file you already have in full. " +
			"The path is confined to the working directory: a path escaping it, a directory, or a binary file (one containing a NUL byte) is an error rather than a partial answer. " +
			"Read a file before editing it — Edit matches exact bytes and will fail on a guess.",
		Properties: map[string]Property{
			"file_path": {Type: "string", Description: "Path to the file: an absolute filesystem path under one of the session roots, or relative to the working directory; a leading `/` not under any root is relative to the session root"},
			"offset":    {Type: "integer", Description: "Optional 1-based line number to start reading from (a Grep line number works as-is); omit to start at line 1"},
			"limit":     {Type: "integer", Description: "Optional number of lines to read (default 2000); the byte bound still applies"},
		},
		Required: []string{"file_path"},
	}
}

// Kind returns the tool kind.
func (r *Read) Kind() Kind { return KindRead }

// Subject returns the permission-rule subject: the path argument resolved
// through the working directory, so a rule keeps matching after a move.
func (r *Read) Subject(args map[string]any) string {
	if s, ok := argString(args, "file_path"); ok {
		return subjectPath(r.Root, r.Cwd, s)
	}
	return ""
}

// Execute reads the file, enforcing root confinement and size limits.
func (r *Read) Execute(ctx context.Context, args map[string]any) (Result, error) {
	pathArg, ok := argString(args, "file_path")
	if !ok || pathArg == "" {
		return Result{}, fmt.Errorf("missing path argument")
	}
	res, err := resolvePath(r.Root, r.Cwd, pathArg)
	if err != nil {
		return Result{}, err
	}
	full := res.Abs()

	f, err := os.Open(full)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("path is a directory")
	}

	max := r.MaxBytes
	if max <= 0 {
		max = 64 * 1024 // 64 KiB default
	}

	meta := map[string]any{"path": res.Rel}
	if l := lexers.Match(filepath.Base(res.Rel)); l != nil {
		meta["lang"] = l.Config().Name
	}

	if r.Verbatim {
		content, err := readVerbatim(f, max)
		if err != nil {
			return Result{}, err
		}
		r.Reads.Note(full)
		return ReadResultMeta(content, meta), nil
	}

	start := int64(1)
	if v, ok := argInt64(args, "offset"); ok {
		if v < 0 {
			return Result{}, fmt.Errorf("offset must not be negative")
		}
		if v > 0 {
			start = v
		}
	}
	limit := int64(readDefaultLines)
	if v, ok := argInt64(args, "limit"); ok {
		if v < 0 {
			return Result{}, fmt.Errorf("limit must not be negative")
		}
		if v > 0 {
			limit = v
		}
	}

	w, err := readWindow(f, start, limit, max)
	if err != nil {
		return Result{}, err
	}
	if w.total > 0 && start > w.total {
		// Past the end is an answer, not an error: an EOF error used to read
		// as a broken call and sent the model round again with new offsets.
		return ReadResultMeta(fmt.Sprintf("[Read: offset %d is past the end of the file (%d lines); nothing more to read]", start, w.total), map[string]any{"path": res.Rel}), nil
	}
	if w.first > 0 {
		meta["start_line"] = int(w.first)
		meta["numbered"] = true
	}
	r.Reads.Note(full)
	return ReadResultMeta(w.body+readTrailer(w.first, w.last, w.total), meta), nil
}

// readWindowResult is one numbered slice of a file.
type readWindowResult struct {
	body        string // numbered lines, no trailing newline
	first, last int64  // line numbers shown; 0 when none
	total       int64  // lines in the whole file
}

// readWindow numbers lines [start, start+limit) of f, stopping early at the
// last whole line that fits in maxBytes, and counts the file's lines to the
// end so the trailer can say where the read stands.
func readWindow(f io.Reader, start, limit, maxBytes int64) (readWindowResult, error) {
	var out readWindowResult
	var body bytes.Buffer
	br := bufio.NewReaderSize(f, 64*1024)
	stopped := false
	var lineNo int64
	for {
		line, clipped, ok, err := nextLine(br, readMaxLineBytes)
		if err != nil {
			return out, err
		}
		if !ok {
			break
		}
		lineNo++
		if stopped || lineNo < start {
			continue
		}
		if lineNo >= start+limit {
			stopped = true
			continue
		}
		if bytes.IndexByte(line, 0) >= 0 {
			return out, fmt.Errorf("binary file (contains NUL bytes)")
		}
		suffix := ""
		if clipped {
			line = line[:runeBoundary(line)]
			suffix = "… [line truncated]"
		}
		row := fmt.Sprintf("%6d\t%s%s\n", lineNo, line, suffix)
		// The first line is always shown, so a tiny cap still answers.
		if out.first > 0 && int64(body.Len()+len(row)) > maxBytes {
			stopped = true
			continue
		}
		body.WriteString(row)
		if out.first == 0 {
			out.first = lineNo
		}
		out.last = lineNo
	}
	out.total = lineNo
	out.body = string(bytes.TrimSuffix(body.Bytes(), []byte{'\n'}))
	return out, nil
}

// nextLine reads one line, without its newline, keeping at most keep bytes of
// it. ok is false only at end of input with nothing read; clipped reports that
// the line was longer than keep. A long line is consumed in buffer-sized
// fragments, so a multi-megabyte single-line file never lands in memory whole.
func nextLine(br *bufio.Reader, keep int) (line []byte, clipped, ok bool, err error) {
	total := 0
	for {
		frag, ferr := br.ReadSlice('\n')
		if len(frag) > 0 {
			ok = true
		}
		content := bytes.TrimSuffix(frag, []byte{'\n'})
		total += len(content)
		if room := keep - len(line); room > 0 {
			line = append(line, content[:min(len(content), room)]...)
		}
		switch ferr {
		case bufio.ErrBufferFull:
			continue
		case nil, io.EOF:
			return line, total > keep, ok, nil
		default:
			return nil, false, false, ferr
		}
	}
}

// readVerbatim returns up to maxBytes of f unchanged, ending a truncated read
// on a whole UTF-8 character.
func readVerbatim(f io.Reader, maxBytes int64) (string, error) {
	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	if bytes.IndexByte(buf[:n], 0) >= 0 {
		return "", fmt.Errorf("binary file (contains NUL bytes)")
	}
	if int64(n) == maxBytes {
		n = runeBoundary(buf[:n])
	}
	return string(buf[:n]), nil
}

// readTrailer tells the model where a partial read stands, so it neither
// re-reads a file it already has in full nor guesses the next offset. A
// whole-file read gets no trailer.
func readTrailer(first, last, total int64) string {
	switch {
	case first == 0 || (first == 1 && last >= total):
		return ""
	case last >= total:
		return fmt.Sprintf("\n[Read: lines %d–%d of %d; end of file]", first, last, total)
	default:
		return fmt.Sprintf("\n[Read: lines %d–%d of %d; truncated — continue with offset=%d]", first, last, total, last+1)
	}
}

// runeBoundary returns the longest prefix length of b that does not end in
// the middle of a UTF-8 encoded character. Invalid UTF-8 is left alone.
func runeBoundary(b []byte) int {
	n := len(b)
	for i := n - 1; i >= 0 && i >= n-utf8.UTFMax; i-- {
		if !utf8.RuneStart(b[i]) {
			continue
		}
		if utf8.FullRune(b[i:n]) {
			return n
		}
		if i == 0 {
			return n
		}
		return i
	}
	return n
}
