package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// This file implements the native tool catalogue: first-class read-only tools
// that replace the raw read-only Bash allowlist for the common investigation
// verbs (search, list, read, transform, git state, and cloud/SaaS read-only
// subcommands). Every tool shells out to a specific binary with a fixed
// argument shape — arbitrary command strings are rejected, arguments are
// passed as Go slices straight to exec.Command (never through a shell), path
// arguments are confined by SanitizePath, and output is capped.

// nativeCommand is the fixed, read-only command shape for one native tool.
type nativeCommand struct {
	// name is the tool name exposed to the model (e.g. "JQ", "Git").
	name string
	// binary is the executable invoked; empty means the lower-cased name.
	binary string
	// desc is the tool description sent to the model.
	desc string
	// props is the JSON-schema property set for the tool.
	props map[string]Property
	// required lists the required argument names.
	required []string
	// build validates the arguments and returns the argv (never through a
	// shell) plus optional stdin data (e.g. the JSON blob JQ filters).
	build func(root string, args map[string]any) (argv []string, stdin string, err error)
	// subject extracts the permission-rule subject from the arguments.
	subject func(args map[string]any) string
}

// Native is a first-class read-only tool backed by a fixed command shape.
type Native struct {
	Root     string
	MaxBytes int
	Timeout  time.Duration
	cmd      nativeCommand
}

// Definition returns the static tool metadata.
func (n *Native) Definition() Definition {
	return Definition{
		Name:        n.cmd.name,
		Description: n.cmd.desc,
		Properties:  n.cmd.props,
		Required:    n.cmd.required,
	}
}

// Kind returns the native read-only kind.
func (n *Native) Kind() Kind { return KindNative }

// Subject returns the permission-rule subject for the arguments.
func (n *Native) Subject(args map[string]any) string {
	if n.cmd.subject == nil {
		return ""
	}
	return n.cmd.subject(args)
}

// Execute runs the fixed command, confining it to Root, capping output, and
// returning a native result. Arguments are validated and shaped by the
// command's build function before execution.
func (n *Native) Execute(ctx context.Context, args map[string]any) (Result, error) {
	argv, stdin, err := n.cmd.build(n.Root, args)
	if err != nil {
		return Result{}, err
	}
	if len(argv) == 0 {
		return Result{}, fmt.Errorf("native tool %s produced no command", n.cmd.name)
	}

	binary := n.cmd.binary
	if binary == "" {
		binary = strings.ToLower(n.cmd.name)
	}

	if n.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, n.Timeout)
		defer cancel()
	}

	ec := exec.CommandContext(ctx, binary, argv...)
	ec.Dir = n.Root
	ec.Env = scrubbedEnv()
	if stdin != "" {
		ec.Stdin = strings.NewReader(stdin)
	}
	ec.WaitDelay = 2 * time.Second
	setProcessGroup(ec)

	maxBytes := n.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	tw := &tailWriter{max: maxBytes, flushEvery: progressFlushInterval}
	ec.Stdout = tw
	ec.Stderr = tw

	if err := ec.Start(); err != nil {
		return Result{}, err
	}
	err = ec.Wait()
	tw.Flush()

	content := tw.Content()
	if ctx.Err() == context.DeadlineExceeded {
		return NativeResult(content + fmt.Sprintf("\n… command timed out after %s", n.Timeout)), nil
	}
	if err != nil {
		content += fmt.Sprintf("\nexit status %d", exitCode(err))
	}
	return NativeResult(content), nil
}

// queryMetacharacters are the bytes rejected in query-language arguments
// (jq/yq filters, sed/awk programs, tr sets). Arguments travel as single argv
// elements to exec.Command — never through a shell — so shell metacharacters
// cannot execute anything; the gate exists to keep a query a single,
// unambiguous argument rather than a smuggling channel for embedded commands
// or NUL/line breaks that other layers reject anyway.
const queryMetacharacters = "\x00\n\r"

// gateQuery rejects query-language arguments that could not be a single
// well-formed argv element.
func gateQuery(q string) error {
	if strings.ContainsAny(q, queryMetacharacters) {
		return fmt.Errorf("query contains disallowed control characters")
	}
	return nil
}

// nativePath validates and confines a path argument, returning the absolute
// path the command may address.
func nativePath(root, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("missing path argument")
	}
	rel, err := SanitizePath(root, raw)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, rel), nil
}

// nativeOptionalPath validates an optional path argument; empty yields the
// root itself so a listing defaults to the working directory.
func nativeOptionalPath(root, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return root, nil
	}
	return nativePath(root, raw)
}

// inputSource resolves the stdin data for transformer tools: the explicit
// input argument wins, then a file path, then empty stdin.
func inputSource(root string, args map[string]any) (string, error) {
	if in, ok := argString(args, "input"); ok {
		return in, nil
	}
	if p, ok := argString(args, "path"); ok && strings.TrimSpace(p) != "" {
		abs, err := nativePath(root, p)
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return "", nil
}

func stringProp(desc string) Property { return Property{Type: "string", Description: desc} }
func boolProp(desc string) Property   { return Property{Type: "boolean", Description: desc} }
func intProp(desc string) Property    { return Property{Type: "integer", Description: desc} }

func argPath(args map[string]any) string {
	s, _ := argString(args, "path")
	return s
}

func pathSubject(args map[string]any) string { return argPath(args) }

// fileTools builds the file-reader natives (Cat, Head, Tail, File, Strings).
func fileTools() []nativeCommand {
	return []nativeCommand{
		{
			name: "Cat", desc: "Print the full contents of a file under the working directory.",
			props:    map[string]Property{"path": stringProp("Relative path to the file")},
			required: []string{"path"},
			build: func(root string, args map[string]any) ([]string, string, error) {
				p, err := nativePath(root, argPath(args))
				return []string{p}, "", err
			},
			subject: pathSubject,
		},
		{
			name: "Head", desc: "Print the first lines of a file under the working directory.",
			props: map[string]Property{
				"path":  stringProp("Relative path to the file"),
				"lines": intProp("Number of lines to print (default 10)"),
			},
			required: []string{"path"},
			build: func(root string, args map[string]any) ([]string, string, error) {
				p, err := nativePath(root, argPath(args))
				if err != nil {
					return nil, "", err
				}
				argv := []string{"-n", "10"}
				if n, ok := argInt64(args, "lines"); ok {
					argv = []string{"-n", fmt.Sprintf("%d", n)}
				}
				return append(argv, p), "", nil
			},
			subject: pathSubject,
		},
		{
			name: "Tail", desc: "Print the last lines of a file under the working directory.",
			props: map[string]Property{
				"path":  stringProp("Relative path to the file"),
				"lines": intProp("Number of lines to print (default 10)"),
			},
			required: []string{"path"},
			build: func(root string, args map[string]any) ([]string, string, error) {
				p, err := nativePath(root, argPath(args))
				if err != nil {
					return nil, "", err
				}
				argv := []string{"-n", "10"}
				if n, ok := argInt64(args, "lines"); ok {
					argv = []string{"-n", fmt.Sprintf("%d", n)}
				}
				return append(argv, p), "", nil
			},
			subject: pathSubject,
		},
		{
			name: "File", desc: "Report the type of a file under the working directory.",
			props:    map[string]Property{"path": stringProp("Relative path to the file")},
			required: []string{"path"},
			build: func(root string, args map[string]any) ([]string, string, error) {
				p, err := nativePath(root, argPath(args))
				return []string{p}, "", err
			},
			subject: pathSubject,
		},
		{
			name: "Strings", desc: "Print the printable strings in a file (useful for binaries).",
			props: map[string]Property{
				"path":   stringProp("Relative path to the file"),
				"minlen": intProp("Minimum string length to print (default 4)"),
			},
			required: []string{"path"},
			build: func(root string, args map[string]any) ([]string, string, error) {
				p, err := nativePath(root, argPath(args))
				if err != nil {
					return nil, "", err
				}
				minlen := "4"
				if n, ok := argInt64(args, "minlen"); ok {
					minlen = fmt.Sprintf("%d", n)
				}
				return []string{"-n", minlen, p}, "", nil
			},
			subject: pathSubject,
		},
	}
}

// listAndFindTools builds LS and Find.
func listAndFindTools() []nativeCommand {
	return []nativeCommand{
		{
			name: "LS", binary: "ls", desc: "List files under a directory in the working directory.",
			props: map[string]Property{
				"path": stringProp("Optional directory to list (defaults to the working directory)"),
				"long": boolProp("When true, use the long listing format"),
			},
			build: func(root string, args map[string]any) ([]string, string, error) {
				p, err := nativeOptionalPath(root, argPath(args))
				if err != nil {
					return nil, "", err
				}
				argv := []string{"-1"}
				if long, _ := argBool(args, "long"); long {
					argv = []string{"-la"}
				}
				return append(argv, p), "", nil
			},
			subject: pathSubject,
		},
		{
			name: "Find", binary: "find", desc: "Find files under a directory, filtered by name and type.",
			props: map[string]Property{
				"path":     stringProp("Optional directory to search (defaults to the working directory)"),
				"name":     stringProp("Optional glob pattern for file names"),
				"type":     stringProp(`Optional type filter: "f" (files) or "d" (directories)`),
				"maxdepth": intProp("Optional maximum recursion depth"),
			},
			build: func(root string, args map[string]any) ([]string, string, error) {
				p, err := nativeOptionalPath(root, argPath(args))
				if err != nil {
					return nil, "", err
				}
				argv := []string{p}
				if n, ok := argInt64(args, "maxdepth"); ok && n > 0 {
					argv = append(argv, "-maxdepth", fmt.Sprintf("%d", n))
				}
				if typ, ok := argString(args, "type"); ok && typ != "" {
					switch typ {
					case "f", "d":
						argv = append(argv, "-type", typ)
					default:
						return nil, "", fmt.Errorf("type must be \"f\" or \"d\"")
					}
				}
				if name, ok := argString(args, "name"); ok && name != "" {
					if err := gateQuery(name); err != nil {
						return nil, "", err
					}
					argv = append(argv, "-name", name)
				}
				return argv, "", nil
			},
			subject: func(args map[string]any) string {
				if n, ok := argString(args, "name"); ok && n != "" {
					return n
				}
				return argPath(args)
			},
		},
	}
}

// gitTool builds the read-only Git tool. The command argument is validated by
// the same read-only git gate the Bash tool uses, so there is one source of
// truth for what a read-only git invocation may contain.
func gitTool() nativeCommand {
	return nativeCommand{
		name: "Git", binary: "git", desc: "Run a read-only git subcommand (status, log, diff, show, rev-parse, ls-files, grep, describe).",
		props: map[string]Property{
			"command": stringProp(`The read-only git command, e.g. "status", "log --oneline -5", or "diff"`),
			"path":    stringProp("Optional directory to run git in (defaults to the working directory)"),
		},
		required: []string{"command"},
		build: func(root string, args map[string]any) ([]string, string, error) {
			cmd, _ := argString(args, "command")
			cmd = strings.TrimSpace(cmd)
			if cmd == "" {
				return nil, "", fmt.Errorf("missing command argument")
			}
			full := "git " + cmd
			if strings.ContainsAny(full, ShellMetacharacters) {
				return nil, "", fmt.Errorf("command contains shell metacharacters")
			}
			if !BashAllowed(full) {
				return nil, "", fmt.Errorf("git command not read-only: %s", cmd)
			}
			fields := strings.Fields(cmd)
			argv := make([]string, 0, len(fields)+2)
			if p, ok := argString(args, "path"); ok && strings.TrimSpace(p) != "" {
				abs, err := nativePath(root, p)
				if err != nil {
					return nil, "", err
				}
				argv = append(argv, "-C", abs)
			}
			return append(argv, fields...), "", nil
		},
		subject: func(args map[string]any) string {
			s, _ := argString(args, "command")
			return s
		},
	}
}

// transformSpec is the per-tool data for one stdin-backed transform tool.
type transformSpec struct {
	name     string
	binary   string
	desc     string
	props    map[string]Property
	required []string
	argv     func(root string, args map[string]any, stdin string) ([]string, string, error)
	subject  func(args map[string]any) string
}

// transformTools builds the stdin-backed transform natives. input is preferred
// over path, so a tool can transform the model's own data without a file.
func transformTools() []nativeCommand {
	specs := []transformSpec{
		{
			name: "JQ", desc: "Transform JSON using a jq filter. Pass JSON via input or path.",
			props: map[string]Property{
				"filter": stringProp("The jq filter expression"),
				"input":  stringProp("JSON text to transform (takes precedence over path)"),
				"path":   stringProp("Optional file holding JSON to transform"),
			},
			required: []string{"filter"},
			argv: func(_ string, args map[string]any, stdin string) ([]string, string, error) {
				f, ok := argString(args, "filter")
				if !ok || strings.TrimSpace(f) == "" {
					return nil, "", fmt.Errorf("missing filter argument")
				}
				if err := gateQuery(f); err != nil {
					return nil, "", err
				}
				return []string{"-r", f}, stdin, nil
			},
			subject: func(args map[string]any) string { s, _ := argString(args, "filter"); return s },
		},
		{
			name: "YQ", desc: "Transform YAML/JSON using a yq expression. Pass data via input or path.",
			props: map[string]Property{
				"filter": stringProp("The yq expression"),
				"input":  stringProp("YAML text to transform (takes precedence over path)"),
				"path":   stringProp("Optional file holding YAML to transform"),
			},
			required: []string{"filter"},
			argv: func(_ string, args map[string]any, stdin string) ([]string, string, error) {
				f, ok := argString(args, "filter")
				if !ok || strings.TrimSpace(f) == "" {
					return nil, "", fmt.Errorf("missing filter argument")
				}
				if err := gateQuery(f); err != nil {
					return nil, "", err
				}
				return []string{"-r", f}, stdin, nil
			},
			subject: func(args map[string]any) string { s, _ := argString(args, "filter"); return s },
		},
		{
			name: "Sed", desc: "Transform text with a sed expression. Pass text via input or path.",
			props: map[string]Property{
				"expression": stringProp("The sed expression, e.g. s/old/new/g"),
				"input":      stringProp("Text to transform (takes precedence over path)"),
				"path":       stringProp("Optional file to transform"),
			},
			required: []string{"expression"},
			argv: func(_ string, args map[string]any, stdin string) ([]string, string, error) {
				e, ok := argString(args, "expression")
				if !ok || strings.TrimSpace(e) == "" {
					return nil, "", fmt.Errorf("missing expression argument")
				}
				if err := gateQuery(e); err != nil {
					return nil, "", err
				}
				return []string{"-E", e}, stdin, nil
			},
			subject: func(args map[string]any) string { s, _ := argString(args, "expression"); return s },
		},
		{
			name: "Awk", desc: "Process text with an awk program. Pass text via input or path.",
			props: map[string]Property{
				"program": stringProp("The awk program, e.g. {print $1}"),
				"input":   stringProp("Text to process (takes precedence over path)"),
				"path":    stringProp("Optional file to process"),
			},
			required: []string{"program"},
			argv: func(_ string, args map[string]any, stdin string) ([]string, string, error) {
				e, ok := argString(args, "program")
				if !ok || strings.TrimSpace(e) == "" {
					return nil, "", fmt.Errorf("missing program argument")
				}
				if err := gateQuery(e); err != nil {
					return nil, "", err
				}
				return []string{e}, stdin, nil
			},
			subject: func(args map[string]any) string { s, _ := argString(args, "program"); return s },
		},
		{
			name: "Cut", desc: "Cut selected fields or columns from each line. Pass text via input or path.",
			props: map[string]Property{
				"fields":    stringProp(`Fields to select, e.g. "1,3" or "2-"`),
				"delimiter": stringProp("Optional field delimiter (default tab)"),
				"input":     stringProp("Text to cut (takes precedence over path)"),
				"path":      stringProp("Optional file to cut"),
			},
			required: []string{"fields"},
			argv: func(_ string, args map[string]any, stdin string) ([]string, string, error) {
				f, ok := argString(args, "fields")
				if !ok || strings.TrimSpace(f) == "" {
					return nil, "", fmt.Errorf("missing fields argument")
				}
				if err := gateQuery(f); err != nil {
					return nil, "", err
				}
				argv := []string{"-f", f}
				if d, ok := argString(args, "delimiter"); ok && d != "" {
					argv = append(argv, "-d", d)
				}
				return argv, stdin, nil
			},
			subject: func(args map[string]any) string { s, _ := argString(args, "fields"); return s },
		},
		{
			name: "Sort", desc: "Sort lines. Pass text via input or path.",
			props: map[string]Property{
				"input":   stringProp("Text to sort (takes precedence over path)"),
				"path":    stringProp("Optional file to sort"),
				"numeric": boolProp("Sort numerically"),
				"reverse": boolProp("Sort in reverse order"),
				"unique":  boolProp("Drop duplicate lines"),
			},
			argv: func(_ string, args map[string]any, stdin string) ([]string, string, error) {
				var argv []string
				if numeric, _ := argBool(args, "numeric"); numeric {
					argv = append(argv, "-n")
				}
				if reverse, _ := argBool(args, "reverse"); reverse {
					argv = append(argv, "-r")
				}
				if unique, _ := argBool(args, "unique"); unique {
					argv = append(argv, "-u")
				}
				return argv, stdin, nil
			},
			subject: func(map[string]any) string { return "" },
		},
		{
			name: "Uniq", desc: "Report or omit repeated lines (adjacent). Pass text via input or path.",
			props: map[string]Property{
				"input": stringProp("Text to process (takes precedence over path)"),
				"path":  stringProp("Optional file to process"),
				"count": boolProp("Prefix lines with the number of occurrences"),
			},
			argv: func(_ string, args map[string]any, stdin string) ([]string, string, error) {
				var argv []string
				if count, _ := argBool(args, "count"); count {
					argv = append(argv, "-c")
				}
				return argv, stdin, nil
			},
			subject: func(map[string]any) string { return "" },
		},
		{
			name: "WC", binary: "wc", desc: "Count lines, words, and bytes. Pass text via input or path.",
			props: map[string]Property{
				"input": stringProp("Text to count (takes precedence over path)"),
				"path":  stringProp("Optional file to count"),
				"lines": boolProp("Count lines"),
				"words": boolProp("Count words"),
				"bytes": boolProp("Count bytes"),
			},
			argv: func(_ string, args map[string]any, stdin string) ([]string, string, error) {
				var argv []string
				if lines, _ := argBool(args, "lines"); lines {
					argv = append(argv, "-l")
				}
				if words, _ := argBool(args, "words"); words {
					argv = append(argv, "-w")
				}
				if bytes, _ := argBool(args, "bytes"); bytes {
					argv = append(argv, "-c")
				}
				if len(argv) == 0 {
					argv = append(argv, "-l")
				}
				return argv, stdin, nil
			},
			subject: func(map[string]any) string { return "" },
		},
		{
			name: "Tr", desc: "Translate or delete characters. Pass text via input or path.",
			props: map[string]Property{
				"set1":  stringProp("The set of characters to translate from"),
				"set2":  stringProp("The set of characters to translate to"),
				"input": stringProp("Text to translate (takes precedence over path)"),
				"path":  stringProp("Optional file to translate"),
			},
			required: []string{"set1", "set2"},
			argv: func(_ string, args map[string]any, stdin string) ([]string, string, error) {
				s1, _ := argString(args, "set1")
				s2, _ := argString(args, "set2")
				if s1 == "" || s2 == "" {
					return nil, "", fmt.Errorf("set1 and set2 are required")
				}
				if err := gateQuery(s1); err != nil {
					return nil, "", err
				}
				if err := gateQuery(s2); err != nil {
					return nil, "", err
				}
				return []string{s1, s2}, stdin, nil
			},
			subject: func(args map[string]any) string { s, _ := argString(args, "set1"); return s },
		},
		{
			name: "Paste", desc: "Merge lines of input side by side. Pass text via input or path.",
			props: map[string]Property{
				"input":     stringProp("Text to merge (takes precedence over path)"),
				"path":      stringProp("Optional file to merge"),
				"delimiter": stringProp("Optional column delimiter (default tab)"),
			},
			argv: func(_ string, args map[string]any, stdin string) ([]string, string, error) {
				var argv []string
				if d, ok := argString(args, "delimiter"); ok && d != "" {
					argv = append(argv, "-d", d)
				}
				return argv, stdin, nil
			},
			subject: func(map[string]any) string { return "" },
		},
		{
			name: "Join", desc: "Join two sorted files on a common field.",
			props: map[string]Property{
				"a":     stringProp("Path to the first sorted file"),
				"b":     stringProp("Path to the second sorted file"),
				"field": stringProp("Optional join field number (default 1)"),
			},
			required: []string{"a", "b"},
			argv: func(root string, args map[string]any, _ string) ([]string, string, error) {
				a, _ := argString(args, "a")
				b, _ := argString(args, "b")
				if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
					return nil, "", fmt.Errorf("a and b paths are required")
				}
				absA, err := nativePath(root, a)
				if err != nil {
					return nil, "", err
				}
				absB, err := nativePath(root, b)
				if err != nil {
					return nil, "", err
				}
				argv := []string{}
				if f, ok := argString(args, "field"); ok && f != "" {
					argv = append(argv, "-1", f, "-2", f)
				}
				return append(argv, absA, absB), "", nil
			},
			subject: func(args map[string]any) string { s, _ := argString(args, "a"); return s },
		},
		{
			name: "Echo", desc: "Print the given text (useful for confirming argument handling).",
			props: map[string]Property{
				"text": stringProp("The text to print"),
			},
			required: []string{"text"},
			argv: func(_ string, args map[string]any, _ string) ([]string, string, error) {
				s, ok := argString(args, "text")
				if !ok {
					return nil, "", fmt.Errorf("missing text argument")
				}
				return []string{s}, "", nil
			},
			subject: func(map[string]any) string { return "" },
		},
		{
			name: "Date", desc: "Print the current date and time (UTC).",
			props: map[string]Property{},
			argv: func(_ string, _ map[string]any, _ string) ([]string, string, error) {
				return []string{"-u"}, "", nil
			},
			subject: func(map[string]any) string { return "" },
		},
		{
			name: "Pwd", binary: "pwd", desc: "Print the current working directory.",
			props: map[string]Property{},
			argv: func(_ string, _ map[string]any, _ string) ([]string, string, error) {
				return nil, "", nil
			},
			subject: func(map[string]any) string { return "" },
		},
		{
			name: "Env", desc: "Print the (credential-scrubbed) environment.",
			props: map[string]Property{},
			argv: func(_ string, _ map[string]any, _ string) ([]string, string, error) {
				return nil, "", nil
			},
			subject: func(map[string]any) string { return "" },
		},
		{
			name: "Diff", desc: "Show the differences between two files or directories.",
			props: map[string]Property{
				"a": stringProp("Path to the first file or directory"),
				"b": stringProp("Path to the second file or directory"),
			},
			required: []string{"a", "b"},
			argv: func(root string, args map[string]any, _ string) ([]string, string, error) {
				a, _ := argString(args, "a")
				b, _ := argString(args, "b")
				if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
					return nil, "", fmt.Errorf("a and b paths are required")
				}
				absA, err := nativePath(root, a)
				if err != nil {
					return nil, "", err
				}
				absB, err := nativePath(root, b)
				if err != nil {
					return nil, "", err
				}
				return []string{"-u", absA, absB}, "", nil
			},
			subject: func(args map[string]any) string { s, _ := argString(args, "a"); return s },
		},
		{
			name: "Cmp", desc: "Compare two files byte by byte.",
			props: map[string]Property{
				"a": stringProp("Path to the first file"),
				"b": stringProp("Path to the second file"),
			},
			required: []string{"a", "b"},
			argv: func(root string, args map[string]any, _ string) ([]string, string, error) {
				a, _ := argString(args, "a")
				b, _ := argString(args, "b")
				if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
					return nil, "", fmt.Errorf("a and b paths are required")
				}
				absA, err := nativePath(root, a)
				if err != nil {
					return nil, "", err
				}
				absB, err := nativePath(root, b)
				if err != nil {
					return nil, "", err
				}
				return []string{absA, absB}, "", nil
			},
			subject: func(args map[string]any) string { s, _ := argString(args, "a"); return s },
		},
	}

	out := make([]nativeCommand, 0, len(specs))
	for _, spec := range specs {
		spec := spec
		out = append(out, nativeCommand{
			name:     spec.name,
			binary:   spec.binary,
			desc:     spec.desc,
			props:    spec.props,
			required: spec.required,
			build: func(root string, args map[string]any) ([]string, string, error) {
				stdin, err := inputSource(root, args)
				if err != nil {
					return nil, "", err
				}
				return spec.argv(root, args, stdin)
			},
			subject: spec.subject,
		})
	}
	return out
}

// localCatalog is every local-utility native command, in catalogue order.
func localCatalog() []nativeCommand {
	out := append(fileTools(), listAndFindTools()...)
	out = append(out, gitTool())
	out = append(out, transformTools()...)
	return out
}

// CatalogueNames returns every native tool name (local and cloud) in
// catalogue order. It is hermetic: no capability detection is performed.
func CatalogueNames() []string {
	var names []string
	for _, c := range localCatalog() {
		names = append(names, c.name)
	}
	for _, c := range cloudCatalog() {
		names = append(names, c.name)
	}
	return names
}

// NativeTools builds the native tools present in caps for the given root. Only
// tools whose binary was detected are returned, so the model can never call a
// tool that is not installed (or, for cloud CLIs, not configured).
func NativeTools(root string, caps Capabilities) []Tool {
	var out []Tool
	for _, c := range localCatalog() {
		if caps.Has(c.name) {
			out = append(out, &Native{Root: root, Timeout: 30 * time.Second, cmd: c})
		}
	}
	for _, c := range cloudCatalog() {
		if caps.Has(c.name) {
			out = append(out, &Native{Root: root, Timeout: 30 * time.Second, cmd: c})
		}
	}
	return out
}

// DefaultWithCaps builds the default tool registry plus every native tool
// present in caps. readOnly removes mutating tools exactly as Default does;
// native tools are read-only by construction and always survive the switch.
func DefaultWithCaps(workdir string, readOnly bool, caps Capabilities) *Registry {
	base := Default(workdir, readOnly)
	extras := NativeTools(workdir, caps)
	if len(extras) == 0 {
		return base
	}
	list := append([]Tool{}, base.tools...)
	list = append(list, extras...)
	reg := NewRegistry(list...)
	if readOnly {
		return reg.ReadOnly()
	}
	return reg
}
