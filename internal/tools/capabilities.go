package tools

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Capabilities is the set of native tools detected at session construction.
// Local utilities need only be present in $PATH; cloud/SaaS CLIs must also
// pass a read-only authentication probe, so an installed-but-unconfigured CLI
// is silently absent (fail closed: the model cannot call it).
type Capabilities struct {
	local map[string]bool
	cloud map[string]bool
}

// Has reports whether the named native tool is available.
func (c Capabilities) Has(name string) bool {
	return c.local[name] || c.cloud[name]
}

// HasBinary reports whether a detected local utility is backed by the named
// binary. The repo-native tools have no capability entry of their own — they
// borrow a local utility's binary (RepoFiles: git, RepoRead: cat) — so they
// gate on the binary rather than on a tool name: a tool that cannot work is
// never offered.
func (c Capabilities) HasBinary(bin string) bool {
	if bin == "" {
		return false
	}
	for _, cmd := range localCatalog() {
		b := cmd.binary
		if b == "" {
			b = strings.ToLower(cmd.name)
		}
		if b == bin && c.local[cmd.name] {
			return true
		}
	}
	return false
}

// IsEmpty reports whether no native tool was detected.
func (c Capabilities) IsEmpty() bool { return len(c.local) == 0 && len(c.cloud) == 0 }

// LocalNames returns the detected local utilities, sorted.
func (c Capabilities) LocalNames() []string { return sortedKeys(c.local) }

// CloudNames returns the detected cloud/SaaS CLIs, sorted.
func (c Capabilities) CloudNames() []string { return sortedKeys(c.cloud) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CapabilityProbe is the injection seam for capability detection. Tests
// substitute fakes so detection never depends on what is installed on the
// machine running the suite.
type CapabilityProbe struct {
	// LookPath resolves a binary in $PATH. nil disables binary detection.
	LookPath func(name string) (string, bool)
	// Run executes a read-only probe command and reports whether it exited
	// cleanly (the authentication check). nil means "no auth probe available".
	Run func(ctx context.Context, name string, args ...string) bool
}

// defaultProbe is the real capability probe: exec.LookPath for presence and a
// credential-scrubbed exec for the authentication check.
func defaultProbe() CapabilityProbe {
	return CapabilityProbe{
		LookPath: func(name string) (string, bool) {
			p, err := exec.LookPath(name)
			return p, err == nil
		},
		Run: func(ctx context.Context, name string, args ...string) bool {
			ec := exec.CommandContext(ctx, name, args...)
			ec.Env = scrubbedEnv()
			return ec.Run() == nil
		},
	}
}

// detectTimeout bounds each auth probe. It must be cheap and non-mutating, so
// every probe is a read-only command with a short timeout; a slow or hanging
// CLI is treated as absent.
const detectTimeout = 2 * time.Second

// DetectDefault runs capability detection against the real environment.
func DetectDefault() Capabilities {
	return Detect(context.Background(), defaultProbe(), detectTimeout)
}

// Detect builds a Capabilities set from a probe. Binary presence is checked
// first; cloud CLIs additionally require their auth probe to exit cleanly.
// Failure is silent — a tool that cannot be verified is simply not offered.
//
// Cloud auth probes run concurrently under a single budget (timeout), so a
// machine with many installed-but-unconfigured CLIs still detects in bounded
// time rather than paying one timeout per CLI.
func Detect(ctx context.Context, probe CapabilityProbe, timeout time.Duration) Capabilities {
	caps := Capabilities{local: map[string]bool{}, cloud: map[string]bool{}}
	if probe.LookPath == nil {
		return caps
	}
	if timeout <= 0 {
		timeout = detectTimeout
	}

	for _, c := range localCatalog() {
		bin := c.binary
		if bin == "" {
			bin = strings.ToLower(c.name)
		}
		if _, ok := probe.LookPath(bin); ok {
			caps.local[c.name] = true
		}
	}

	// Partition cloud CLIs into presence-only and auth-probe groups.
	var probed []cloudSpec
	for _, cs := range cloudSpecs {
		if _, ok := probe.LookPath(cs.binary); !ok {
			continue
		}
		if len(cs.probe) == 0 {
			// Presence-only CLI: installed is enough to offer the read-only
			// subcommands, which fail at runtime if unconfigured.
			caps.cloud[cs.name] = true
			continue
		}
		if probe.Run != nil {
			probed = append(probed, cs)
		}
	}

	if len(probed) > 0 {
		pctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		sem := make(chan struct{}, 4)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, cs := range probed {
			wg.Add(1)
			go func(cs cloudSpec) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if probe.Run(pctx, cs.binary, cs.probe...) {
					mu.Lock()
					caps.cloud[cs.name] = true
					mu.Unlock()
				}
			}(cs)
		}
		wg.Wait()
	}
	return caps
}

// cloudSpec describes one cloud/SaaS CLI: its binary, an authentication probe
// (nil means presence-only), and the read-only subcommand prefixes the tool
// may run. Only read-only subcommands are ever offered; anything else is
// rejected by the build function, so a cloud tool can never mutate resources.
type cloudSpec struct {
	name     string
	binary   string
	desc     string
	probe    []string
	prefixes []string
	// gate is an optional second check, run after a prefix match. It exists
	// for CLIs where the prefix names the family but not whether the call
	// reads or writes — `gh api` is the same prefix for a GET and for a POST.
	gate func(cmd string) error
	// classify marks a CLI whose output is text written off this machine, so
	// its results go through the security classifier rather than being
	// sanitised only.
	classify bool
}

// cloudSpecs is the capability-detection table for network-reachable CLIs.
// A CLI with an empty prefix list is detected but never offered a first-class
// tool (fail closed: we cannot enumerate its read-only surface confidently).
var cloudSpecs = []cloudSpec{
	{
		name: "GH", binary: "gh",
		desc:  "Query GitHub (read-only): auth status, repo/PR/issue/release/run views and search.",
		probe: []string{"auth", "status"},
		prefixes: []string{
			"auth status",
			"repo view", "repo list",
			"pr list", "pr view", "pr diff", "pr checks",
			"issue list", "issue view",
			"release list", "release view",
			"run list", "run view", "run log",
			"search prs", "search issues", "search repos", "search code",
			"api graphql",
			"api",
		},
		gate:     ghAPIGate,
		classify: true,
	},
	{
		name: "AWS", binary: "aws",
		desc:  "Query AWS (read-only): caller identity, S3 listing, and describe/list operations.",
		probe: []string{"sts", "get-caller-identity"},
		prefixes: []string{
			"sts get-caller-identity",
			"s3 ls",
			"s3api list-buckets", "s3api list-objects", "s3api get-object",
			"ec2 describe-instances", "ec2 describe-security-groups", "ec2 describe-vpcs", "ec2 describe-subnets",
			"cloudformation describe-stacks", "cloudformation list-stacks",
			"logs describe-log-groups",
			"iam list-users", "iam list-roles",
			"lambda list-functions",
			"eks list-clusters",
		},
	},
	{
		name: "AZ", binary: "az",
		desc:  "Query Azure (read-only): account, resource group, and service list/show operations.",
		probe: []string{"account", "show"},
		prefixes: []string{
			"account show", "account list",
			"group list", "group show",
			"vm list", "vm show",
			"aks list", "aks show",
			"acr list", "acr show",
			"resource list", "resource show",
			"config show",
		},
	},
	{
		name: "GCloud", binary: "gcloud",
		desc:  "Query Google Cloud (read-only): config, project, and resource list operations.",
		probe: []string{"config", "get-value", "account"},
		prefixes: []string{
			"config get-value account", "config list", "config get-value project",
			"projects list", "projects describe",
			"compute instances list", "compute instances describe",
			"auth list",
		},
	},
	{
		name: "Kubectl", binary: "kubectl",
		desc:  "Query a Kubernetes cluster (read-only): get, describe, logs, and config inspection.",
		probe: []string{"config", "current-context"},
		prefixes: []string{
			"config current-context", "config get-contexts", "config view",
			"get", "describe", "logs", "explain", "top", "api-resources", "api-versions",
		},
	},
	{
		name: "Terraform", binary: "terraform",
		desc:  "Inspect Terraform state and configuration (read-only): plan, show, validate, state list/show, output.",
		probe: []string{"version"},
		prefixes: []string{
			"version", "validate", "fmt -check", "plan", "show", "state list", "state show", "output", "providers",
		},
	},
	{
		name: "Pulumi", binary: "pulumi",
		desc:  "Inspect Pulumi stacks (read-only): whoami, stack list/export, preview, config, about.",
		probe: []string{"whoami"},
		prefixes: []string{
			"whoami", "about", "stack ls", "stack export", "preview", "config", "org ls",
		},
	},
	{
		name: "Heroku", binary: "heroku",
		desc:  "Query Heroku (read-only): auth, app, config, log, process, release, and status views.",
		probe: []string{"auth:whoami"},
		prefixes: []string{
			"auth:whoami", "apps", "apps:info", "config", "logs", "ps", "releases", "status",
		},
	},
	{
		name: "Fly", binary: "flyctl",
		desc:  "Query Fly.io (read-only): auth, app, status, org, region, and volume list operations.",
		probe: []string{"auth", "whoami"},
		prefixes: []string{
			"auth whoami", "apps list", "status", "orgs list", "regions list", "volumes list",
		},
	},
	{
		name: "Vercel", binary: "vercel",
		desc:  "Query Vercel (read-only): whoami, list, inspect, project/env/domain listing.",
		probe: []string{"whoami"},
		prefixes: []string{
			"whoami", "ls", "list", "inspect", "project ls", "env ls", "domains ls",
		},
	},
	{
		name: "Netlify", binary: "netlify",
		desc:     "Query Netlify (read-only): status and site/deploy listing.",
		probe:    []string{"status"},
		prefixes: []string{"status", "sites:list", "deploys:list"},
	},
	{
		name: "Doctl", binary: "doctl",
		desc:  "Query DigitalOcean (read-only): account, droplet, cluster, balance, and region views.",
		probe: []string{"account", "get"},
		prefixes: []string{
			"account get", "compute droplet list", "kubernetes cluster list",
			"balance get", "region list",
		},
	},
	{
		name: "Glab", binary: "glab",
		desc:  "Query GitLab (read-only): auth status and MR/issue/pipeline/release views.",
		probe: []string{"auth", "status"},
		prefixes: []string{
			"auth status", "mr list", "mr view", "issue list", "issue view",
			"pipeline list", "release list",
		},
		classify: true,
	},
	{
		name: "Stripe", binary: "stripe",
		desc:     "Inspect Stripe configuration (read-only).",
		probe:    []string{"config", "--list"},
		prefixes: []string{"version", "config --list"},
	},
	{
		name: "OnePassword", binary: "op",
		desc:     "Check 1Password CLI authentication (read-only identity only; secret contents are never exposed).",
		probe:    []string{"whoami"},
		prefixes: []string{"whoami", "account list"},
	},
	{
		name: "Bitwarden", binary: "bw",
		desc:     "Check Bitwarden CLI status (read-only identity only; secret contents are never exposed).",
		probe:    []string{"status"},
		prefixes: []string{"status"},
	},
	// Presence-only CLIs. They are detected in $PATH but have no safe,
	// confidently-enumerated read-only subcommand surface, so no first-class
	// tool is offered for them. They remain listed here so capability
	// detection reports them accurately and the catalogue is extensible.
	{name: "LinodeCLI", binary: "linode-cli", desc: "Linode CLI (detected, no read-only tool offered)."},
	{name: "Eksctl", binary: "eksctl", desc: "eksctl (detected, no read-only tool offered)."},
	{name: "Twilio", binary: "twilio", desc: "Twilio CLI (detected, no read-only tool offered)."},
	{name: "AzureDevOps", binary: "az", desc: "Azure DevOps CLI (detected via az, no read-only tool offered)."},
	{name: "CircleCI", binary: "circleci", desc: "CircleCI CLI (detected, no read-only tool offered)."},
}

// cloudCatalog renders the cloud specs that have a read-only subcommand
// allowlist into first-class native tools.
func cloudCatalog() []nativeCommand {
	var out []nativeCommand
	for _, cs := range cloudSpecs {
		if len(cs.prefixes) == 0 {
			continue
		}
		out = append(out, cloudNative(cs))
	}
	return out
}

// cloudNative builds one cloud CLI native tool. The command argument must be a
// read-only subcommand matching an allowed prefix; it is passed straight to
// exec.Command as argv, never through a shell.
func cloudNative(cs cloudSpec) nativeCommand {
	cmd := nativeCommand{
		name:   cs.name,
		binary: cs.binary,
		desc:   cs.desc,
		kind:   KindNative,
		props: map[string]Property{
			"command": stringProp("The read-only " + cs.binary + " command to run"),
		},
		required: []string{"command"},
		build: func(root string, args map[string]any) ([]string, string, error) {
			cmd, _ := argString(args, "command")
			cmd = strings.TrimSpace(cmd)
			if cmd == "" {
				return nil, "", fmt.Errorf("missing command argument")
			}
			if strings.ContainsAny(cmd, ShellMetacharacters) {
				return nil, "", fmt.Errorf("command contains shell metacharacters")
			}
			if !cloudAllowed(cs, cmd) {
				return nil, "", fmt.Errorf("command not in the read-only %s allowlist: %s", cs.binary, cmd)
			}
			if cs.gate != nil {
				if err := cs.gate(cmd); err != nil {
					return nil, "", fmt.Errorf("command failed the read-only gate for %s: %w", cs.binary, err)
				}
			}
			return strings.Fields(cmd), "", nil
		},
		subject: func(args map[string]any) string {
			s, _ := argString(args, "command")
			return s
		},
	}
	if cs.classify {
		cmd.kind = KindRemote
	}
	return cmd
}

// cloudAllowed reports whether cmd is one of the spec's read-only prefixes.
func cloudAllowed(cs cloudSpec, cmd string) bool {
	for _, p := range cs.prefixes {
		if cmd == p || strings.HasPrefix(cmd, p+" ") {
			return true
		}
	}
	return false
}
