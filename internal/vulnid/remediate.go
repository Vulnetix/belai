package vulnid

import (
	"strings"
	"unicode"
)

// remediationPromptMax bounds the prompt IsRemediation reads. A request that
// names an advisory and asks for a fix is short; a long document that happens
// to mention one is not a remediation request.
const remediationPromptMax = 4096

// fixWords are the words that ask for a vulnerability to be fixed.
var fixWords = map[string]bool{
	"fix": true, "fixes": true, "fixing": true, "remediate": true, "remediation": true,
	"patch": true, "patching": true, "bump": true, "upgrade": true, "update": true,
	"resolve": true, "mitigate": true, "address": true,
}

// askWords turn a request into a question about the advisory, which is not a
// request to change the repository.
var askWords = map[string]bool{
	"explain": true, "what": true, "why": true, "how": true, "describe": true,
	"summarize": true, "summarise": true, "tell": true, "whether": true, "if": true,
}

// IsRemediation reports whether a prompt asks for the repository to be fixed
// for an advisory: the prepared remediation prompt, a request that names an
// identifier and asks to fix, patch, bump or remediate it, or the bare link
// and vdb command the row's copy buttons produce. It is a harness check on the
// sanitised prompt, never a model's; a question about an advisory is not one.
func IsRemediation(prompt string) bool {
	p := strings.TrimSpace(prompt)
	if p == "" || len(p) > remediationPromptMax {
		return false
	}
	// Find skips an identifier inside a URL, so the console's own link is
	// opened up first; any other URL stays a longer token.
	ids := Find(strings.ReplaceAll(p, ConsoleBase, " "), 8)
	if len(ids) == 0 {
		return false
	}
	if strings.HasPrefix(p, "Remediate ") {
		return true
	}
	words := strings.FieldsFunc(strings.ToLower(p), func(r rune) bool {
		return !unicode.IsLetter(r)
	})
	asked, fix := false, false
	for _, w := range words {
		asked = asked || askWords[w]
		fix = fix || fixWords[w]
	}
	if asked {
		return false
	}
	return fix || onlyRowText(p, ids)
}

// onlyRowText reports whether nothing is left of the prompt once the
// identifiers, their console links and their vdb commands are taken out: the
// text a person pastes after copying a row.
func onlyRowText(p string, ids []string) bool {
	for _, id := range ids {
		for _, s := range []string{URL(id), Command(id), id} {
			if s != "" {
				p = strings.ReplaceAll(p, s, "")
			}
		}
	}
	p = strings.ReplaceAll(p, "vulnetix vdb vuln", "")
	return strings.TrimFunc(p, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	}) == ""
}
