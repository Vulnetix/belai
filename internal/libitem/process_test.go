package libitem

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// js builds a JSON document from a body of fields, adding a valid name and
// command unless the body supplies its own.
func js(fields string) []byte {
	return []byte("{" + fields + "}")
}

func repeatJSON(s string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = s
	}
	return strings.Join(parts, ",")
}

func TestValidateProcess(t *testing.T) {
	base := `"name":"web","command":"python3"`
	long := func(n int) string { return strings.Repeat("x", n) }
	envN := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`"VAR_%d":"v"`, i)
		}
		return strings.Join(parts, ",")
	}
	cases := []struct {
		name string
		doc  string
		err  string // substring of the refusal; "" means accepted
	}{
		{"minimal", base, ""},
		{"every field", `"name":"web","command":"python3","args":["-m","http.server"],"options":[{"name":"--port","value":"8080"},{"name":"-v"}],"env":{"PORT":"8080","TOKEN":"env:WEB_TOKEN"},"cwd":"~/srv","user":"www-data","stdout":{"mode":"file","path":"/var/log/web.out"},"stderr":{"mode":"stdout"},"enabled":false,"order":40`, ""},

		// Names.
		{"missing name", `"command":"x"`, "name is required"},
		{"uppercase name", `"name":"Web","command":"x"`, "lowercase"},
		{"name with a slash", `"name":"a/b","command":"x"`, "lowercase"},
		{"name at the limit", `"name":"` + long(64) + `","command":"x"`, ""},
		{"name over the limit", `"name":"` + long(65) + `","command":"x"`, "lowercase"},
		{"name with dots and underscores", `"name":"my_web.v2","command":"x"`, ""},

		// Command.
		{"missing command", `"name":"web"`, "command is required"},
		{"empty command", `"name":"web","command":""`, "command is required"},
		{"command at the limit", `"name":"web","command":"` + long(1024) + `"`, ""},
		{"command over the limit", `"name":"web","command":"` + long(1025) + `"`, "command is 1025 bytes"},
		{"command with a newline", `"name":"web","command":"a\nb"`, "holds a control character"},
		{"command with a carriage return", `"name":"web","command":"a\rb"`, "holds a control character"},
		{"command with a NUL", `"name":"web","command":"a\u0000b"`, "holds a control character"},
		{"command that is not a string", `"name":"web","command":["a"]`, "must be a string"},

		// Args.
		{"args at the limit", base + `,"args":[` + repeatJSON(`"a"`, 64) + `]`, ""},
		{"args over the limit", base + `,"args":[` + repeatJSON(`"a"`, 65) + `]`, "args has 65 entries"},
		{"an arg at the limit", base + `,"args":["` + long(1024) + `"]`, ""},
		{"an arg over the limit", base + `,"args":["` + long(1025) + `"]`, "args[0] is 1025 bytes"},
		{"an arg with a newline is allowed", base + `,"args":["a\nb"]`, ""},
		{"an arg with a NUL", base + `,"args":["a\u0000b"]`, "holds a control character"},
		{"args that are not strings", base + `,"args":[1]`, "must be a string"},
		{"args that are not a list", base + `,"args":"x"`, "must be a list"},

		// Options.
		{"options at the limit", base + `,"options":[` + repeatJSON(`{"name":"-v"}`, 64) + `]`, ""},
		{"options over the limit", base + `,"options":[` + repeatJSON(`{"name":"-v"}`, 65) + `]`, "options has 65 entries"},
		{"a short option", base + `,"options":[{"name":"-v"}]`, ""},
		{"a long option", base + `,"options":[{"name":"--log-level.x_y"}]`, ""},
		{"an option that starts with a digit", base + `,"options":[{"name":"--1x"}]`, ""},
		{"an option with no dash", base + `,"options":[{"name":"v"}]`, "is not an option name like -v or --verbose"},
		{"an option that is only dashes", base + `,"options":[{"name":"--"}]`, "is not an option name like -v or --verbose"},
		{"an option with three dashes", base + `,"options":[{"name":"---x"}]`, "is not an option name like -v or --verbose"},
		{"an option with a space", base + `,"options":[{"name":"--a b"}]`, "is not an option name like -v or --verbose"},
		{"an option that is an equals form", base + `,"options":[{"name":"--a=b"}]`, "is not an option name like -v or --verbose"},
		{"an option with a non-ASCII letter", base + `,"options":[{"name":"-é"}]`, "is not an option name like -v or --verbose"},
		{"an empty option name", base + `,"options":[{"name":""}]`, "name is required"},
		{"an option value at the limit", base + `,"options":[{"name":"-a","value":"` + long(1024) + `"}]`, ""},
		{"an option value over the limit", base + `,"options":[{"name":"-a","value":"` + long(1025) + `"}]`, "options[0].value is 1025 bytes"},
		{"an empty option value is a value", base + `,"options":[{"name":"-a","value":""}]`, ""},
		{"an option with an unknown key", base + `,"options":[{"name":"-a","values":["x"]}]`, `unknown field "values"`},
		{"an option value with a NUL", base + `,"options":[{"name":"-a","value":"x\u0000"}]`, "holds a control character"},

		// Env.
		{"env at the limit", base + `,"env":{` + envN(64) + `}`, ""},
		{"env over the limit", base + `,"env":{` + envN(65) + `}`, "env has 65 variables"},
		{"an env value at the limit", base + `,"env":{"A":"` + long(4096) + `"}`, ""},
		{"an env value over the limit", base + `,"env":{"A":"` + long(4097) + `"}`, "env.A is 4097 bytes"},
		{"an env name with a digit first", base + `,"env":{"1A":"x"}`, "env name"},
		{"an env name with a hyphen", base + `,"env":{"A-B":"x"}`, "env name"},
		{"an env name that is empty", base + `,"env":{"":"x"}`, "env name"},
		{"a lowercase env name", base + `,"env":{"path_x":"x"}`, ""},
		{"an env value with a NUL", base + `,"env":{"A":"x\u0000"}`, "holds a control character"},
		{"an env value that is not a string", base + `,"env":{"A":1}`, "env.A must be a string"},
		{"a literal under a name with SECRET", base + `,"env":{"MY_SECRET":"hunter2"}`, "looks like a secret"},
		{"a literal under a name with token", base + `,"env":{"gh_token":"x"}`, "looks like a secret"},
		{"a literal under a name with PASSWORD", base + `,"env":{"DB_PASSWORD":"x"}`, "looks like a secret"},
		{"a literal under a name with passwd", base + `,"env":{"PASSWD_FILE":"x"}`, "looks like a secret"},
		{"a literal under a name with api_key", base + `,"env":{"OPENAI_API_KEY":"x"}`, "looks like a secret"},
		{"a literal under a name with apikey", base + `,"env":{"APIKEY":"x"}`, "looks like a secret"},
		{"a literal under a name with credential", base + `,"env":{"AWS_CREDENTIALS":"x"}`, "looks like a secret"},
		{"a literal under a name with private", base + `,"env":{"PRIVATE_KEY":"x"}`, "looks like a secret"},
		{"a secret name copying a host variable", base + `,"env":{"MY_SECRET":"env:HOST_SECRET"}`, ""},
		{"a secret name with an empty reference", base + `,"env":{"MY_SECRET":"env:"}`, "is not env: followed by a variable name"},
		{"a secret name with a bad reference", base + `,"env":{"MY_SECRET":"env:1x"}`, "is not env: followed by a variable name"},
		{"a reference with a space", base + `,"env":{"A":"env:X Y"}`, "is not env: followed by a variable name"},
		{"a reference is case sensitive", base + `,"env":{"MY_TOKEN":"ENV:HOST"}`, "looks like a secret"},
		{"a plain name copying a host variable", base + `,"env":{"HOME_DIR":"env:HOME"}`, ""},
		{"a value that merely mentions env:", base + `,"env":{"A":"see env:HOME"}`, ""},

		// Cwd.
		{"an absolute cwd", base + `,"cwd":"/srv/app"`, ""},
		{"a home cwd", base + `,"cwd":"~"`, ""},
		{"a home-relative cwd", base + `,"cwd":"~/app"`, ""},
		{"a project-relative cwd", base + `,"cwd":"services/api"`, ""},
		{"a cwd that climbs out of the project is a start-time matter, not the validator's", base + `,"cwd":"../x"`, ""},
		{"a cwd that climbs in the middle", base + `,"cwd":"a/../../x"`, ""},
		{"an absolute cwd may use ..", base + `,"cwd":"/srv/../opt"`, ""},
		{"a cwd with a newline", base + `,"cwd":"a\nb"`, "control character"},
		{"a cwd at the limit", base + `,"cwd":"` + long(1024) + `"`, ""},
		{"a cwd over the limit", base + `,"cwd":"` + long(1025) + `"`, "cwd is 1025 bytes"},

		// User.
		{"a user", base + `,"user":"www-data"`, ""},
		{"an empty user is no user", base + `,"user":""`, ""},
		{"a user with a capital", base + `,"user":"Root"`, "user"},
		{"a user that starts with a digit", base + `,"user":"1a"`, "user"},
		{"a user at the limit", base + `,"user":"` + long(32) + `"`, ""},
		{"a user over the limit", base + `,"user":"` + long(33) + `"`, "user"},
		{"a user with a space", base + `,"user":"a b"`, "user"},

		// Redirects.
		{"stdout log", base + `,"stdout":{"mode":"log"}`, ""},
		{"stdout discard", base + `,"stdout":{"mode":"discard"}`, ""},
		{"stdout file", base + `,"stdout":{"mode":"file","path":"out.log"}`, ""},
		{"stdout append", base + `,"stdout":{"mode":"append","path":"~/out.log"}`, ""},
		{"stdout cannot merge into itself", base + `,"stdout":{"mode":"stdout"}`, "only valid for stderr"},
		{"stderr stdout", base + `,"stderr":{"mode":"stdout"}`, ""},
		{"stderr file", base + `,"stderr":{"mode":"file","path":"/tmp/e"}`, ""},
		{"a file with no path", base + `,"stdout":{"mode":"file"}`, "stdout.path is required"},
		{"an append with an empty path", base + `,"stderr":{"mode":"append","path":""}`, "stderr.path is required"},
		{"a discard with a path", base + `,"stdout":{"mode":"discard","path":"x"}`, "only allowed for mode file or append"},
		{"a log with a path", base + `,"stdout":{"mode":"log","path":"x"}`, "only allowed for mode file or append"},
		{"stdout mode with a path", base + `,"stderr":{"mode":"stdout","path":"x"}`, "only allowed for mode file or append"},
		{"an unknown mode", base + `,"stdout":{"mode":"tee"}`, "must be log, discard, file, append or stdout"},
		{"no mode", base + `,"stdout":{}`, "must be log, discard, file, append or stdout"},
		{"a mode in the wrong case", base + `,"stdout":{"mode":"LOG"}`, "must be log, discard"},
		{"a redirect with an unknown key", base + `,"stdout":{"mode":"log","tee":true}`, `unknown field "tee"`},
		{"a redirect path that climbs is a start-time matter too", base + `,"stdout":{"mode":"file","path":"../x"}`, ""},
		{"a redirect path at the limit", base + `,"stdout":{"mode":"file","path":"` + long(1024) + `"}`, ""},
		{"a redirect path over the limit", base + `,"stdout":{"mode":"file","path":"` + long(1025) + `"}`, "path is 1025 bytes"},
		{"a redirect that is not an object", base + `,"stdout":"log"`, "must be an object"},

		// Enabled and order.
		{"enabled true", base + `,"enabled":true`, ""},
		{"enabled that is not a bool", base + `,"enabled":"yes"`, "must be true or false"},
		{"order 0", base + `,"order":0`, ""},
		{"order 999", base + `,"order":999`, ""},
		{"order 1000", base + `,"order":1000`, "must be from 0 to 999"},
		{"order -1", base + `,"order":-1`, "must be from 0 to 999"},
		{"order as a string", base + `,"order":"5"`, "must be a whole number"},
		{"order with a fraction", base + `,"order":1.5`, "must be a whole number"},

		// The schema.
		{"an unknown key", base + `,"restart":"always"`, `unknown field "restart"`},
		{"a key in the wrong case", `"Name":"web","command":"x"`, `unknown field "Name"`},
		{"a shell key", base + `,"shell":"sh"`, `unknown field "shell"`},
		{"user and cwd as numbers", base + `,"cwd":5`, "must be a string"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, err := Validate(Process, js(c.doc))
			if c.err == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if it.Kind != Process || it.Name == "" || len(it.SHA256) != 64 {
					t.Errorf("item = %+v", it)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err = %v, want one naming %q", err, c.err)
			}
			if !IsRefusal(err) {
				t.Errorf("%v is not a refusal", err)
			}
		})
	}
}

func TestProcessDocumentSizeLimit(t *testing.T) {
	big := `{"command":"x","name":"web","args":[` + repeatJSON(`"`+strings.Repeat("a", 1000)+`"`, 20) + `]}`
	if _, err := Validate(Process, []byte(big)); err == nil || !strings.Contains(err.Error(), "the most is 16384") {
		t.Errorf("a document over 16 KiB: %v", err)
	}
	// Whitespace the canonical form removes does not count against the limit.
	spaced := `{"command":"x","name":"web"` + strings.Repeat(" ", 20<<10) + `}`
	if _, err := Validate(Process, []byte(spaced)); err != nil {
		t.Errorf("padding whitespace: %v", err)
	}
}

func TestProcessArgv(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name string
		doc  ProcessDoc
		want []string
	}{
		{"command only", ProcessDoc{Command: "ls"}, []string{"ls"}},
		{"args", ProcessDoc{Command: "ls", Args: []string{"-l", "/tmp"}}, []string{"ls", "-l", "/tmp"}},
		{"options come before args", ProcessDoc{Command: "srv", Args: []string{"run"}, Options: []ProcessOption{{Name: "--port", Value: s("80")}}}, []string{"srv", "--port", "80", "run"}},
		{"an option without a value", ProcessDoc{Command: "srv", Options: []ProcessOption{{Name: "-v"}}}, []string{"srv", "-v"}},
		{"an empty value is its own element", ProcessDoc{Command: "srv", Options: []ProcessOption{{Name: "--x", Value: s("")}}}, []string{"srv", "--x", ""}},
		{"options keep their order", ProcessDoc{Command: "srv", Options: []ProcessOption{{Name: "-b"}, {Name: "-a", Value: s("1")}, {Name: "-c"}}}, []string{"srv", "-b", "-a", "1", "-c"}},
		{"a value is never split", ProcessDoc{Command: "srv", Options: []ProcessOption{{Name: "--msg", Value: s("a b; rm -rf /")}}}, []string{"srv", "--msg", "a b; rm -rf /"}},
	}
	for _, c := range cases {
		got := c.doc.Argv()
		if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", c.want) {
			t.Errorf("%s: argv = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestProcessDisplayQuotesWhatNeedsIt(t *testing.T) {
	d := ProcessDoc{Command: "srv", Args: []string{"plain-word_1.2/x", "has space", "it's", "", "a;b", "$HOME"}}
	want := `srv plain-word_1.2/x 'has space' 'it'\''s' '' 'a;b' '$HOME'`
	if got := d.Display(); got != want {
		t.Errorf("Display = %s, want %s", got, want)
	}
}

func TestProcessDefaults(t *testing.T) {
	f, five := false, 5
	if !(ProcessDoc{}).IsEnabled() || (ProcessDoc{Enabled: &f}).IsEnabled() {
		t.Error("IsEnabled")
	}
	if (ProcessDoc{}).OrderOr(7) != 7 || (ProcessDoc{Order: new(int)}).OrderOr(7) != 7 || (ProcessDoc{Order: &five}).OrderOr(7) != 5 {
		t.Error("OrderOr")
	}
}

func TestEnvRefAndLooksSecret(t *testing.T) {
	for in, want := range map[string][3]any{
		"env:HOME":  {"HOME", true, true},
		"env:":      {"", true, false},
		"env:1X":    {"1X", true, false},
		"env:A B":   {"A B", true, false},
		"env:a_b9":  {"a_b9", true, true},
		"ENV:HOME":  {"", false, false},
		"literal":   {"", false, false},
		" env:HOME": {"", false, false},
	} {
		other, ref, ok := EnvRef(in)
		if other != want[0] || ref != want[1] || ok != want[2] {
			t.Errorf("EnvRef(%q) = %q %v %v, want %v", in, other, ref, ok, want)
		}
	}
	for _, n := range []string{"SECRET", "mysecret", "TOKEN", "Github_Token", "PASSWORD", "passwd", "API_KEY", "api-key", "APIKEY", "ApiKey", "CREDENTIAL", "private_key"} {
		if !LooksSecret(n) {
			t.Errorf("LooksSecret(%q) = false", n)
		}
	}
	for _, n := range []string{"PATH", "HOME", "PORT", "LOG_LEVEL", "KEY", "PUBLIC_KEY", "TOK"} {
		if LooksSecret(n) {
			t.Errorf("LooksSecret(%q) = true", n)
		}
	}
}

func TestLegacyProcess(t *testing.T) {
	d, err := LegacyProcess("web", "python3 -m http.server\nsleep 1", 40, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := encodeCanonical(map[string]any{"name": d.Name, "command": d.Command, "args": d.Args, "order": *d.Order, "enabled": *d.Enabled})
	it, err := Validate(Process, b)
	if err != nil || it.Name != "web" {
		t.Fatalf("%v %+v", err, it)
	}
	got, _ := ParseProcess(it.Doc)
	if got.Command != "sh" || len(got.Args) != 2 || got.Args[0] != "-c" || got.Args[1] != "python3 -m http.server\nsleep 1" || got.IsEnabled() || got.OrderOr(0) != 40 {
		t.Errorf("legacy doc = %+v", got)
	}
	// An enabled legacy entry with no order carries neither field.
	d2, _ := LegacyProcess("web", "x", 0, true)
	if d2.Enabled != nil || d2.Order != nil {
		t.Errorf("defaults were written: %+v", d2)
	}
	// A body too long for an argument cannot travel.
	if _, err := LegacyProcess("web", strings.Repeat("x", 1025), 0, true); err == nil {
		t.Error("a 1025-byte body was accepted")
	}
	if _, err := LegacyProcess("Web", "x", 0, true); err == nil {
		t.Error("a bad name was accepted")
	}
}

// The same document in any spelling has one hash.
func TestProcessHashIsStable(t *testing.T) {
	a, err := Validate(Process, []byte(`{"name":"web","command":"x","args":["a","b"],"env":{"B":"2","A":"1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Validate(Process, []byte("{\n  \"env\": {\"A\":\"1\", \"B\":\"2\"},\n  \"args\": [ \"a\", \"b\" ],\n  \"command\": \"x\",\n  \"name\": \"web\"\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 || string(a.Doc) != `{"args":["a","b"],"command":"x","env":{"A":"1","B":"2"},"name":"web"}`+"\n" {
		t.Fatalf("%s\n%s", a.Doc, b.Doc)
	}
	// Argument order is part of the document: reordering args changes the hash.
	c, _ := Validate(Process, []byte(`{"name":"web","command":"x","args":["b","a"],"env":{"B":"2","A":"1"}}`))
	if c.SHA256 == a.SHA256 {
		t.Error("a reordered args list hashed the same")
	}
}

func TestNormalizeProcessFile(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		slug    string
		order   int
		enabled bool
		want    string // the canonical document; "" means a refusal naming err
		err     string
	}{
		{"a document as installed is unchanged", `{"command":"x","name":"web"}`, "web", 10, true, `{"command":"x","name":"web"}`, ""},
		{"the name may be left out", `{"command":"x"}`, "web", 10, true, `{"command":"x","name":"web"}`, ""},
		{"the slug is the name", `{"command":"x","name":"other"}`, "web", 10, true, `{"command":"x","name":"web"}`, ""},
		{"a disabled file is disabled", `{"command":"x","name":"web"}`, "web", 10, false, `{"command":"x","enabled":false,"name":"web"}`, ""},
		{"a disabled file overrides enabled true", `{"command":"x","enabled":true,"name":"web"}`, "web", 10, false, `{"command":"x","enabled":false,"name":"web"}`, ""},
		{"an enabled file drops enabled false", `{"command":"x","enabled":false,"name":"web"}`, "web", 10, true, `{"command":"x","name":"web"}`, ""},
		{"an enabled true the file agrees with is kept", `{"command":"x","enabled":true,"name":"web"}`, "web", 10, true, `{"command":"x","enabled":true,"name":"web"}`, ""},
		{"an order that agrees is kept", `{"command":"x","name":"web","order":10}`, "web", 10, true, `{"command":"x","name":"web","order":10}`, ""},
		{"an order that disagrees is the file's", `{"command":"x","name":"web","order":50}`, "web", 10, true, `{"command":"x","name":"web","order":10}`, ""},
		{"order 0 means none and stays", `{"command":"x","name":"web","order":0}`, "web", 10, true, `{"command":"x","name":"web","order":0}`, ""},
		{"an absent order stays absent", `{"command":"x","name":"web"}`, "web", 330, true, `{"command":"x","name":"web"}`, ""},
		{"an invalid document is refused", `{"name":"web"}`, "web", 10, true, "", "command is required"},
		{"not JSON", `echo hi`, "web", 10, true, "", "not valid JSON"},
		{"an unknown key is refused", `{"command":"x","bogus":1}`, "web", 10, true, "", "unknown field"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, err := NormalizeProcessFile([]byte(c.raw), c.slug, c.order, c.enabled)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want one naming %q", err, c.err)
				}
				return
			}
			if err != nil || string(it.Doc) != c.want+"\n" || it.Name != c.slug {
				t.Fatalf("got %q, %v; want %q", it.Doc, err, c.want)
			}
		})
	}
}

// The page that describes the process document states the numbers and patterns
// the validator enforces.
func TestLibraryItemsPageStatesTheProcessRules(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/library-items.md")), " ")
	for _, want := range []string{
		"1 to 1024 bytes, no control character",
		"at most 64 strings of at most 1024 bytes",
		"at most 64 of `{name, value?}`",
		"is at most 128 bytes, `value` is a string of at most 1024 bytes",
		"at most 64 variables",
		"a value is at most 4096 bytes",
		"at most 1024 bytes: absolute",
		"whole number 0 to 999",
		"`" + optionNameRE.String() + "`",
		"`" + envNameRE.String() + "`",
		"`" + userRE.String() + "`",
		"`" + secretNameRE.String() + "`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/library-items.md does not say %q", want)
		}
	}
	for _, mode := range []string{RedirectLog, RedirectDiscard, RedirectFile, RedirectAppend, RedirectStdout} {
		if !strings.Contains(doc, "`"+mode+"`") {
			t.Errorf("docs/library-items.md does not name the redirect mode %q", mode)
		}
	}
	if MaxProcessCommand != 1024 || MaxProcessArgs != 64 || MaxProcessArg != 1024 || MaxProcessOptions != 64 || MaxProcessOptName != 128 ||
		MaxProcessEnv != 64 || MaxProcessEnvName != 128 || MaxProcessEnvVal != 4096 || MaxProcessPath != 1024 || MaxProcessOrder != 999 {
		t.Error("a process limit changed; update docs/library-items.md and this test")
	}
}
