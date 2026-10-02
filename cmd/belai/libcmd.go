package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
)

// `belai <kind> list|validate|import|export` for every kind of library item
// (docs/library-items.md): the same documents the website's library keeps, read
// and written through internal/libstore so a file means one thing here, in a
// sync and on the website. A file is a document of the kind, "-" is standard
// input or output, and every file is validated whole before anything is written.

// libraryCommands are the kinds that have a command, by the word the user types.
var libraryCommands = map[string]libitem.Kind{
	"skill":   libitem.Skill,
	"prompt":  libitem.Prompt,
	"process": libitem.Process,
}

// libraryNoun is the plural the usage text uses.
var libraryNoun = map[libitem.Kind]string{
	libitem.Skill:   "skills",
	libitem.Prompt:  "prompts",
	libitem.Process: "processes",
}

func libraryUsage(kind libitem.Kind) string {
	word := string(kind)
	return fmt.Sprintf(`usage: belai %[1]s <command> [flags] [args]

  list [-json]              the %[2]s this host holds, with the hash the library compares
  validate FILE             check a %[1]s document without saving it
  import [-force] FILE      validate a %[1]s document and save it (-force replaces one of that name)
  export [-force] NAME [FILE]
                            write a %[1]s as the library keeps it (no FILE or - is standard output;
                            -force replaces an existing FILE)

FILE - means standard input. See docs/library-items.md.
`, word, libraryNoun[kind])
}

// runLibraryCLI implements `belai <kind> …` and returns the exit code.
func runLibraryCLI(kind libitem.Kind, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, libraryUsage(kind))
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	code, err := libraryCommand(kind, args[0], args[1:], stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func libraryCommand(kind libitem.Kind, cmd string, rest []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet(string(kind)+" "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch cmd {
	case "list":
		asJSON := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(rest); err != nil {
			return 2, nil
		}
		return libraryList(kind, *asJSON, stdout, stderr)

	case "validate", "import":
		force := fs.Bool("force", false, "replace an item of the same name")
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			if cmd == "import" {
				return 2, fmt.Errorf("usage: belai %s import [-force] FILE", kind)
			}
			return 2, fmt.Errorf("usage: belai %s validate FILE", kind)
		}
		data, err := readLibraryFile(kind, fs.Arg(0), stdin)
		if err != nil {
			return 1, err
		}
		if cmd == "validate" {
			it, err := libitem.Validate(kind, data)
			if err != nil {
				return 1, fmt.Errorf("%s: %w", fs.Arg(0), err)
			}
			fmt.Fprintf(stdout, "%s: valid %s %q (%d bytes, sha256 %s)\n", fs.Arg(0), kind, it.Name, len(it.Doc), it.SHA256[:12])
			return 0, nil
		}
		res, err := libstore.Install(kind, data, libstore.InstallOptions{Overwrite: *force})
		switch {
		case errors.Is(err, libstore.ErrExists):
			return 1, fmt.Errorf("this host already has that %s; pass -force to replace it", kind)
		case err != nil:
			return 1, fmt.Errorf("%s: %w", fs.Arg(0), err)
		}
		verb := "saved"
		if res.Replaced {
			verb = "replaced"
		}
		fmt.Fprintf(stdout, "%s %s\n", verb, res.Where)
		return 0, nil

	case "export":
		force := fs.Bool("force", false, "replace an existing FILE")
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() < 1 || fs.NArg() > 2 {
			return 2, fmt.Errorf("usage: belai %s export [-force] NAME [FILE]", kind)
		}
		it, err := libstore.Get(kind, fs.Arg(0))
		if errors.Is(err, libstore.ErrNotFound) {
			return 1, fmt.Errorf("this host has no %s named %q (see `belai %s list`)", kind, fs.Arg(0), kind)
		}
		if err != nil {
			return 1, err
		}
		if fs.NArg() == 1 || fs.Arg(1) == "-" {
			_, err := stdout.Write(it.Doc)
			return 0, err
		}
		if err := writeLibraryFile(fs.Arg(1), it.Doc, *force); err != nil {
			return 1, err
		}
		fmt.Fprintf(stderr, "wrote %s (%d bytes, sha256 %s)\n", fs.Arg(1), len(it.Doc), it.SHA256[:12])
		return 0, nil
	}
	fmt.Fprint(stderr, libraryUsage(kind))
	return 2, nil
}

func libraryList(kind libitem.Kind, asJSON bool, stdout, stderr io.Writer) (int, error) {
	items, skipped, err := libstore.List(kind)
	if err != nil {
		return 1, err
	}
	if asJSON {
		type row struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		}
		out := struct {
			Items   []row `json:"items"`
			Skipped []struct {
				Name   string `json:"name"`
				Reason string `json:"reason"`
			} `json:"skipped"`
		}{Items: []row{}}
		for _, it := range items {
			out.Items = append(out.Items, row{it.Name, it.SHA256, len(it.Doc)})
		}
		for _, s := range skipped {
			out.Skipped = append(out.Skipped, struct {
				Name   string `json:"name"`
				Reason string `json:"reason"`
			}{s.Name, s.Reason})
		}
		return 0, jsonOut(stdout, out)
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tBYTES\tSHA256")
	for _, it := range items {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", it.Name, len(it.Doc), it.SHA256[:12])
	}
	if err := tw.Flush(); err != nil {
		return 1, err
	}
	for _, s := range skipped {
		fmt.Fprintf(stderr, "skipped %s: %s\n", cleanLog(s.Name), cleanLog(s.Reason))
	}
	return 0, nil
}

// readLibraryFile reads a document from a file or, for "-", standard input,
// never more than twice the kind's limit.
func readLibraryFile(kind libitem.Kind, path string, stdin io.Reader) ([]byte, error) {
	limit := int64(kind.MaxBytes())*2 + 1
	var r io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) >= limit {
		return nil, fmt.Errorf("%s is larger than a %s may be (%d bytes)", path, kind, kind.MaxBytes())
	}
	return data, nil
}

// writeLibraryFile writes an exported document next to its destination and moves
// it into place, refusing an existing file unless force is set. The file is
// private: a document can hold text its author kept to themselves.
func writeLibraryFile(path string, data []byte, force bool) error {
	if fi, err := os.Lstat(path); err == nil {
		if !force {
			return fmt.Errorf("%s exists; pass -force to replace it", path)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+hex.EncodeToString(rnd[:])+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
