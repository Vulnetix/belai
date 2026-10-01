package tags

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Def is one vocabulary topic as the rest of the harness sees it.
type Def struct {
	ID     string
	Label  string
	Domain string
	// Prio is 1 for a common topic, 3 for a niche one. A decision backend is
	// asked about priority 1 first when it cannot take them all.
	Prio int
}

type topicDef struct {
	Def
	keys  []termKey
	paths []string
	res   []*regexp.Regexp
}

type termKey struct {
	key    string
	strong bool
}

// domains lists the vocabulary groups in the order a decision call visits them.
var domains = []struct {
	name string
	data string
}{
	{"security", vocabSecurity},
	{"languages", vocabLanguages},
	{"web-api", vocabWebAPI},
	{"data", vocabData},
	{"cloud", vocabCloud},
	{"delivery", vocabDelivery},
	{"quality", vocabQuality},
	{"operations", vocabOps},
	{"ai", vocabAI},
	{"frontend", vocabFrontend},
	{"mobile-systems", vocabMobile},
	{"docs", vocabDocs},
	{"compliance", vocabCompliance},
	{"product", vocabProduct},
	{"code", vocabCode},
	{"platform", vocabPlatform},
}

var (
	loadOnce sync.Once
	topics   []topicDef
	byID     map[string]int
	// termIndex maps a stemmed word sequence to the topics that list it.
	termIndex map[string][]termHit
	maxGram   int
)

type termHit struct {
	topic  int
	strong bool
}

func load() {
	loadOnce.Do(func() {
		byID = map[string]int{}
		termIndex = map[string][]termHit{}
		maxGram = 1
		for _, d := range domains {
			for _, line := range strings.Split(d.data, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				f := strings.SplitN(line, "|", 5)
				if len(f) < 4 {
					panic("tags: bad vocabulary line: " + line)
				}
				t := topicDef{Def: Def{ID: f[0], Label: f[1], Domain: d.name, Prio: int(f[2][0] - '0')}}
				if _, dup := byID[t.ID]; dup {
					panic("tags: duplicate topic id " + t.ID)
				}
				seen := map[string]bool{}
				for _, raw := range strings.Split(f[3], ",") {
					raw = strings.TrimSpace(raw)
					if raw == "" {
						continue
					}
					if strings.HasPrefix(raw, "p:") {
						t.paths = append(t.paths, strings.ToLower(strings.TrimPrefix(raw, "p:")))
						continue
					}
					strong := strings.HasPrefix(raw, "!")
					raw = strings.TrimPrefix(raw, "!")
					words := words(raw)
					if len(words) == 0 {
						continue
					}
					if len(words) > maxGram {
						maxGram = len(words)
					}
					key := strings.Join(words, " ")
					if seen[key] {
						continue
					}
					seen[key] = true
					t.keys = append(t.keys, termKey{key: key, strong: strong})
				}
				if len(f) == 5 && strings.TrimSpace(f[4]) != "" {
					for _, expr := range strings.Split(f[4], ";;") {
						t.res = append(t.res, regexp.MustCompile(strings.TrimSpace(expr)))
					}
				}
				idx := len(topics)
				byID[t.ID] = idx
				for _, k := range t.keys {
					termIndex[k.key] = append(termIndex[k.key], termHit{topic: idx, strong: k.strong})
				}
				topics = append(topics, t)
			}
		}
	})
}

// Vocabulary returns every topic, in domain order and then file order.
func Vocabulary() []Def {
	load()
	out := make([]Def, len(topics))
	for i, t := range topics {
		out[i] = t.Def
	}
	return out
}

// Lookup returns the topic with the id.
func Lookup(id string) (Def, bool) {
	load()
	i, ok := byID[id]
	if !ok {
		return Def{}, false
	}
	return topics[i].Def, true
}

// Domains lists the domain names in the order a decision call visits them.
func Domains() []string {
	out := make([]string, len(domains))
	for i, d := range domains {
		out[i] = d.name
	}
	return out
}

// Candidates picks up to limit topic ids for one decision call. Topics the
// deterministic detector found come first, best score first, because those are
// the ones a backend's verdict changes most. The rest are filled in by
// priority and then round robin across the domains, so a document about
// something the detector missed still gets a broad spread of questions.
func Candidates(found []Topic, limit int) []string {
	load()
	if limit <= 0 {
		return nil
	}
	taken := map[string]bool{}
	var out []string
	sorted := append([]Topic(nil), found...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })
	for _, t := range sorted {
		if _, ok := byID[t.ID]; ok && !taken[t.ID] && len(out) < limit {
			taken[t.ID] = true
			out = append(out, t.ID)
		}
	}
	for prio := 1; prio <= 3 && len(out) < limit; prio++ {
		buckets := make([][]string, len(domains))
		for _, t := range topics {
			if t.Prio != prio || taken[t.ID] {
				continue
			}
			for di, d := range domains {
				if d.name == t.Domain {
					buckets[di] = append(buckets[di], t.ID)
					break
				}
			}
		}
		for round := 0; len(out) < limit; round++ {
			progressed := false
			for _, b := range buckets {
				if round < len(b) && len(out) < limit {
					out = append(out, b[round])
					progressed = true
				}
			}
			if !progressed {
				break
			}
		}
	}
	return out
}
