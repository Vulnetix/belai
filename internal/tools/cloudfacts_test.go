package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/factspec"
)

// echoScript prints its arguments and the environment variables facts set, so
// a test sees exactly what a tool's subprocess received.
const echoScript = `#!/bin/sh
echo "args=$*"
env | sort | grep -E '^(KUBECONFIG|AZURE_[A-Z_]+|ARM_[A-Z_]+|CLOUDSDK_[A-Z_]+|GOOGLE_[A-Z_]+|GH_[A-Z_]+|GITLAB_[A-Z_]+|PULUMI_[A-Z_]+|HEROKU_APP|FLY_APP|VERCEL_[A-Z_]+|NETLIFY_SITE_ID|DIGITALOCEAN_CONTEXT|OP_ACCOUNT|VULNETIX_[A-Z_]+|TF_[A-Za-z_]+)='
exit 0
`

func fakeCLIs(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(echoScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestEveryFactBindingReachesItsTool runs each well-known fact through the real
// tool and a fake binary, so the table, the tool name and the argv or
// environment it produces are checked together.
func TestEveryFactBindingReachesItsTool(t *testing.T) {
	fakeCLIs(t, "kubectl", "az", "gcloud", "gh", "glab", "pulumi", "heroku", "flyctl", "vercel", "netlify", "doctl", "op", "terraform")
	cases := []struct {
		tool, cmd string
		facts     map[string][]string
		want      []string
		notWant   []string
	}{
		{"Kubectl", "get pods", map[string][]string{"kubectl_context": {"prod"}, "kubectl_namespace": {"web"}, "kubectl_kubeconfig": {"/etc/kube/config"}},
			[]string{"args=get pods --context prod --namespace web", "KUBECONFIG=/etc/kube/config"}, nil},
		{"Kubectl", "get pods -A", map[string][]string{"kubectl_namespace": {"web"}}, []string{"args=get pods -A"}, []string{"--namespace"}},
		{"Kubectl", "get pods -n other", map[string][]string{"kubectl_namespace": {"web"}}, []string{"args=get pods -n other"}, []string{"--namespace"}},
		{"Kubectl", "config view", map[string][]string{"kubectl_namespace": {"web"}}, []string{"args=config view"}, []string{"--namespace"}},
		{"AZ", "vm list", map[string][]string{"azure_subscription": {"sub1"}, "azure_resource_group": {"rg"}, "azure_location": {"uksouth"}, "azure_config_dir": {"/etc/azure"}},
			[]string{"args=vm list --subscription sub1", "AZURE_DEFAULTS_GROUP=rg", "AZURE_DEFAULTS_LOCATION=uksouth", "AZURE_CONFIG_DIR=/etc/azure"}, nil},
		{"AZ", "account list", map[string][]string{"azure_subscription": {"sub1"}}, []string{"args=account list"}, []string{"--subscription"}},
		{"GCloud", "projects list", map[string][]string{"gcloud_project": {"my-project-123"}, "gcloud_account": {"me@example.com"}, "gcloud_region": {"europe-west2"},
			"gcloud_zone": {"europe-west2-a"}, "gcloud_impersonate_service_account": {"sa@my-project-123.iam.gserviceaccount.com"}, "gcloud_configuration": {"work"}},
			[]string{"CLOUDSDK_CORE_PROJECT=my-project-123", "CLOUDSDK_CORE_ACCOUNT=me@example.com", "CLOUDSDK_COMPUTE_REGION=europe-west2",
				"CLOUDSDK_COMPUTE_ZONE=europe-west2-a", "CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT=sa@my-project-123.iam.gserviceaccount.com", "CLOUDSDK_ACTIVE_CONFIG_NAME=work"}, nil},
		{"GH", "pr list", map[string][]string{"github_repo": {"org/repo"}, "github_host": {"ghe.example.com"}}, []string{"GH_REPO=org/repo", "GH_HOST=ghe.example.com"}, nil},
		{"Glab", "mr list", map[string][]string{"gitlab_host": {"gl.example.com"}, "gitlab_repo": {"g/r"}, "gitlab_group": {"g"}},
			[]string{"GITLAB_HOST=gl.example.com", "GITLAB_REPO=g/r", "GITLAB_GROUP=g"}, nil},
		{"Pulumi", "whoami", map[string][]string{"pulumi_stack": {"dev"}, "pulumi_backend_url": {"s3://bucket/state"}}, []string{"PULUMI_STACK=dev", "PULUMI_BACKEND_URL=s3://bucket/state"}, nil},
		{"Heroku", "apps", map[string][]string{"heroku_app": {"myapp"}}, []string{"HEROKU_APP=myapp"}, nil},
		{"Fly", "status", map[string][]string{"fly_app": {"myapp"}}, []string{"FLY_APP=myapp"}, nil},
		{"Vercel", "ls", map[string][]string{"vercel_org_id": {"org1"}, "vercel_project_id": {"prj1"}, "vercel_scope": {"team"}},
			[]string{"args=ls --scope team", "VERCEL_ORG_ID=org1", "VERCEL_PROJECT_ID=prj1"}, nil},
		{"Vercel", "whoami", map[string][]string{"vercel_scope": {"team"}}, []string{"args=whoami"}, []string{"--scope"}},
		{"Vercel", "ls --scope other", map[string][]string{"vercel_scope": {"team"}}, []string{"args=ls --scope other"}, []string{"--scope team"}},
		{"Netlify", "status", map[string][]string{"netlify_site_id": {"site1"}}, []string{"NETLIFY_SITE_ID=site1"}, nil},
		{"Doctl", "account get", map[string][]string{"doctl_context": {"work"}}, []string{"DIGITALOCEAN_CONTEXT=work"}, nil},
		{"OnePassword", "whoami", map[string][]string{"onepassword_account": {"my.1password.com"}}, []string{"OP_ACCOUNT=my.1password.com"}, nil},
		{"Terraform", "plan", map[string][]string{"azure_subscription": {"sub1"}, "azure_tenant": {"00000000-0000-0000-0000-000000000000"}, "gcloud_project": {"my-project-123"},
			"gcloud_region": {"europe-west2"}, "gcloud_zone": {"europe-west2-a"}, "tf_var_zones": {"a", "b"}, "tf_var_environment": {"prod"}},
			[]string{"ARM_SUBSCRIPTION_ID=sub1", "ARM_TENANT_ID=00000000-0000-0000-0000-000000000000", "GOOGLE_PROJECT=my-project-123",
				"GOOGLE_REGION=europe-west2", "GOOGLE_ZONE=europe-west2-a", `TF_VAR_zones=["a","b"]`, "TF_VAR_environment=prod"}, nil},
	}
	for _, c := range cases {
		t.Run(c.tool+" "+c.cmd, func(t *testing.T) {
			n := nativeNamed(t, c.tool, hubWith(c.facts))
			res, err := n.Execute(context.Background(), map[string]any{"command": c.cmd})
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range c.want {
				if !strings.Contains(res.Content, w) {
					t.Errorf("output lacks %q:\n%s", w, res.Content)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(res.Content, w) {
					t.Errorf("output has %q:\n%s", w, res.Content)
				}
			}
		})
	}
}

// A fact for one tool does not reach another, and no facts leave a tool's
// environment as it was.
func TestFactsStayWithTheirTool(t *testing.T) {
	fakeCLIs(t, "kubectl", "gh")
	hub := hubWith(map[string][]string{"github_repo": {"org/repo"}, "kubectl_context": {"prod"}})
	res, err := nativeNamed(t, "Kubectl", hub).Execute(context.Background(), map[string]any{"command": "get pods"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "GH_REPO") {
		t.Errorf("a GitHub fact reached kubectl:\n%s", res.Content)
	}
	res, err = nativeNamed(t, "GH", hub).Execute(context.Background(), map[string]any{"command": "pr list"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "--context") {
		t.Errorf("a kubectl fact reached gh:\n%s", res.Content)
	}
}

func TestPinnedFlagsAreRefusedThroughTheTools(t *testing.T) {
	fakeCLIs(t, "kubectl", "gcloud", "doctl", "op", "az")
	for _, c := range []struct {
		tool, cmd string
		facts     map[string][]string
	}{
		{"Kubectl", "get pods --context other", map[string][]string{"kubectl_context": {"prod"}}},
		{"Kubectl", "get pods --kubeconfig /tmp/x", map[string][]string{"kubectl_kubeconfig": {"/etc/kube/config"}}},
		{"GCloud", "projects list --project other", map[string][]string{"gcloud_project": {"my-project-123"}}},
		{"GCloud", "config list --account x@y.com", map[string][]string{"gcloud_account": {"me@example.com"}}},
		{"Doctl", "account get --context other", map[string][]string{"doctl_context": {"work"}}},
		{"OnePassword", "whoami --account other", map[string][]string{"onepassword_account": {"my.1password.com"}}},
		{"AZ", "vm list --subscription other", map[string][]string{"azure_subscription": {"sub1"}}},
	} {
		if _, err := nativeNamed(t, c.tool, hubWith(c.facts)).Execute(context.Background(), map[string]any{"command": c.cmd}); err == nil {
			t.Errorf("%s %q accepted while the fact pins it", c.tool, c.cmd)
		}
		// The same flag is the model's to choose when no fact pins it.
		if _, err := nativeNamed(t, c.tool, hubWith(nil)).Execute(context.Background(), map[string]any{"command": c.cmd}); err != nil && !strings.Contains(c.cmd, "--kubeconfig") {
			t.Errorf("%s %q without the fact: %v", c.tool, c.cmd, err)
		}
	}
}

func TestVulnetixFactsSetItsEnvironment(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "vulnetix")
	if err := os.WriteFile(bin, []byte(echoScript), 0o755); err != nil {
		t.Fatal(err)
	}
	v := &Vulnetix{Root: dir, Binary: bin, Timeout: 30 * time.Second, Cloud: hubWith(map[string][]string{
		"vulnetix_org_id": {"org-1"}, "vulnetix_project": {"web"}, "vulnetix_namespace": {"acme"}, "vulnetix_environment": {"prod"},
	})}
	res, err := v.Execute(context.Background(), map[string]any{"command": "version"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"VULNETIX_ORG_ID=org-1", "VULNETIX_PROJECT=web", "VULNETIX_NAMESPACE=acme", "VULNETIX_ENVIRONMENT=prod"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("output lacks %q:\n%s", want, res.Content)
		}
	}
	// No facts, no variables.
	v.Cloud = nil
	res, err = v.Execute(context.Background(), map[string]any{"command": "version"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "VULNETIX_ORG_ID") {
		t.Errorf("variables without facts:\n%s", res.Content)
	}
}

// Calls that run side by side share one assumption.
func TestConcurrentCallsAssumeTheRoleOnce(t *testing.T) {
	f := newFakeAWS(t)
	n := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_role_arn": {testRole}}))
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := n.Execute(context.Background(), logsCall(""))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := f.count(t, "sts assume-role"); got != 1 {
		t.Errorf("assume-role ran %d times for concurrent calls, want 1", got)
	}
}

func TestMissingAWSCLIIsReportedAndFailsClosed(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	withRole := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_role_arn": {testRole}}))
	_, err := withRole.Execute(context.Background(), logsCall(""))
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("a missing aws CLI should be named, got %v", err)
	}
	// The account guard cannot be checked without the CLI, so the call stops.
	guarded := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_account_id": {"123456789012"}}))
	if _, err := guarded.Execute(context.Background(), logsCall("")); err == nil || !strings.Contains(err.Error(), "aws_account_id") {
		t.Errorf("an unverifiable account must refuse the call, got %v", err)
	}
}

// With two declared roles and no argument there is no default: AWS and
// Terraform run as the ambient identity and assume nothing.
func TestTwoDeclaredRolesAreNotADefault(t *testing.T) {
	f := newFakeAWS(t)
	hub := hubWith(map[string][]string{"aws_role_arn": {testRole, otherRole}})
	res, err := nativeNamed(t, "Terraform", hub).Execute(context.Background(), map[string]any{"command": "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "creds=yes") || f.count(t, "sts assume-role") != 0 {
		t.Errorf("terraform assumed a role with two declared:\n%s", res.Content)
	}
}

// A declared role that is not the one passed still asks only for the one that
// is undeclared, and non-AWS tools never ask.
func TestOnlyTheAWSToolAsks(t *testing.T) {
	for _, name := range []string{"Terraform", "Kubectl", "GH", "Glab", "AZ", "GCloud"} {
		n := nativeNamed(t, name, hubWith(nil))
		if AlwaysAsksCall(n, map[string]any{"command": "x", "role_arn": otherRole}) {
			t.Errorf("%s asks", name)
		}
	}
}

// Every tool the fact table binds to is a tool the registry can build, so a
// renamed tool cannot strand a fact.
func TestFactToolNamesAreCatalogueTools(t *testing.T) {
	names := CatalogueNames()
	check := func(where, tool string) {
		if !slices.Contains(names, tool) {
			t.Errorf("%s names %q, which is not a catalogue tool", where, tool)
		}
	}
	for _, e := range factspec.Table() {
		for _, b := range e.Env {
			check(e.Key+" env", b.Tool)
		}
		for _, b := range e.Flags {
			check(e.Key+" flag", b.Tool)
		}
		for _, o := range e.Overrides {
			check(e.Key+" override", o.Tool)
		}
	}
	for _, tool := range factspec.RefusedTools() {
		check("refused flags", tool)
	}
}

// The AWS prefixes that read logs and metrics are the read-only half of those
// services, and the cloud tools that are classified are the ones whose output
// other parties write.
func TestCloudToolKinds(t *testing.T) {
	for name, want := range map[string]Kind{"AWS": KindRemote, "GH": KindRemote, "Glab": KindRemote, "Kubectl": KindNative, "Terraform": KindNative, "GCloud": KindNative} {
		if got := nativeNamed(t, name, nil).Kind(); got != want {
			t.Errorf("%s kind = %s, want %s", name, got, want)
		}
	}
}

// Date prints ISO 8601 and the Unix time, so a model building a --start-time
// reads the epoch instead of computing it.
func TestDatePrintsISOAndUnixSeconds(t *testing.T) {
	res, err := nativeNamed(t, "Date", nil).Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z) \(Unix (\d+)\)\s*$`).FindStringSubmatch(res.Content)
	if m == nil {
		t.Fatalf("Date output = %q", res.Content)
	}
	at, err := time.Parse(time.RFC3339, m[1])
	if err != nil {
		t.Fatal(err)
	}
	unix, _ := strconv.ParseInt(m[2], 10, 64)
	if at.Unix() != unix {
		t.Errorf("ISO %s is Unix %d, but the output says %d", m[1], at.Unix(), unix)
	}
	if d := time.Since(at); d < -time.Minute || d > time.Minute {
		t.Errorf("Date is %s from now", d)
	}
}
