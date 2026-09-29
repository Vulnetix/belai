package tools

import "path"

// swapTable maps the program a Bash command runs to the builtin tools that
// might do the same job. It is a shortlist, not a decision: it bounds what
// the decision backend is asked about (a handful of candidates, so even the
// local model answers quickly) and keeps the swap to commands whose job a
// builtin plausibly has. The decision itself is Jev's rating and the fast
// model's replan, both validated by the harness.
var swapTable = map[string][]string{
	"cat": {"Read", "Cat"}, "head": {"Read", "Head"}, "tail": {"Read", "Tail"},
	"grep": {"Grep"}, "egrep": {"Grep"}, "fgrep": {"Grep"}, "rg": {"Grep"},
	"find": {"Glob", "Find"}, "fd": {"Glob", "Find"}, "ls": {"LS", "Glob"},
	"wc": {"WC"}, "sort": {"Sort"}, "uniq": {"Uniq"}, "cut": {"Cut"}, "tr": {"Tr"},
	"paste": {"Paste"}, "join": {"Join"}, "sed": {"Sed"}, "awk": {"Awk"},
	"jq": {"JQ"}, "yq": {"YQ"}, "diff": {"Diff"}, "cmp": {"Cmp"},
	"git": {"Git"}, "curl": {"WebFetch"}, "wget": {"WebFetch"},
	"date": {"Date"}, "pwd": {"Pwd"}, "env": {"Env"}, "printenv": {"Env"},
	"echo": {"Echo"}, "file": {"File"}, "strings": {"Strings"},
}

// SwapCandidates returns the names of the builtin tools that might replace a
// single simple command, in preference order, or nil when none is plausible.
// argv is the command's words with quotes removed; the program is matched by
// its base name so /usr/bin/grep counts as grep.
func SwapCandidates(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	names := swapTable[path.Base(argv[0])]
	if len(names) == 0 {
		return nil
	}
	return append([]string(nil), names...)
}
