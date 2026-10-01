package factspec

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/proc"
)

func facts(kv ...string) map[string][]string {
	m := map[string][]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = append(m[kv[i]], kv[i+1])
	}
	return m
}

const roleARN = "arn:aws:iam::123456789012:role/ops/ReadOnly"

func TestFreeFactsAreAccepted(t *testing.T) {
	f := facts("environment", "prod", "owner", "platform team", "anything_at_all", "x", "anything_at_all", "y")
	if err := Validate(f); err != nil {
		t.Fatalf("free facts rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tooMany := map[string][]string{}
	for i := 0; i < MaxKeys+1; i++ {
		tooMany[fmt.Sprintf("k%d", i)] = []string{"v"}
	}
	var longList []string
	for i := 0; i < MaxValues+1; i++ {
		longList = append(longList, "v")
	}
	cases := map[string]map[string][]string{
		"upper key":        facts("Env", "x"),
		"leading digit":    facts("1env", "x"),
		"dash key":         facts("my-key", "x"),
		"secret suffix":    facts("db_password", "x"),
		"token suffix":     facts("deploy_token", "x"),
		"empty value":      facts("environment", ""),
		"multi line":       facts("environment", "a\nb"),
		"control rune":     facts("environment", "a\x07b"),
		"padded":           facts("environment", " a"),
		"access key shape": facts("note", "use AKIAABCDEFGHIJKLMNOP here"),
		"long value":       facts("note", strings.Repeat("a", MaxValueRunes+1)),
		"too many values":  {"note": longList},
		"too many keys":    tooMany,
		"no value":         {"note": nil},
		"role not arn":     facts("aws_role_arn", "ReadOnly"),
		"account short":    facts("aws_account_id", "1234"),
		"two regions":      facts("aws_region", "us-east-1", "aws_region", "eu-west-1"),
		"bad region":       facts("aws_region", "mars"),
		"seconds low":      facts("aws_session_seconds", "60"),
		"dir escapes":      facts("terraform_dir", "../x"),
		"dir absolute":     facts("terraform_dir", "/etc"),
		"dir option":       facts("terraform_dir", "-x"),
		"kubeconfig rel":   facts("kubectl_kubeconfig", "kube/config"),
		"kubeconfig up":    facts("kubectl_kubeconfig", "/home/a/../b"),
		"backend scheme":   facts("pulumi_backend_url", "http://example.com"),
		"tf var name":      facts("tf_var_1x", "x"),
		"tenant":           facts("azure_tenant", "not-a-uuid"),
		"gcp account":      facts("gcloud_account", "nobody"),
	}
	for name, f := range cases {
		if err := Validate(f); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateAcceptsWellKnown(t *testing.T) {
	f := map[string][]string{
		"aws_role_arn":        {roleARN, "arn:aws:iam::123456789012:role/Builder"},
		"aws_account_id":      {"123456789012"},
		"aws_region":          {"eu-west-2"},
		"aws_profile":         {"ops"},
		"aws_external_id":     {"ext-1234"},
		"aws_session_seconds": {"3600"},
		"terraform_dir":       {"infra/prod"},
		"terraform_workspace": {"prod"},
		"tf_var_environment":  {"prod"},
		"tf_var_zones":        {"a", "b"},
		"kubectl_context":     {"prod"},
		"kubectl_namespace":   {"web"},
		"kubectl_kubeconfig":  {"~/.kube/config"},
		"azure_subscription":  {"00000000-0000-0000-0000-000000000000"},
		"azure_tenant":        {"00000000-0000-0000-0000-000000000000"},
		"gcloud_project":      {"my-project-123"},
		"gcloud_account":      {"me@example.com"},
		"gcloud_region":       {"europe-west2"},
		"github_repo":         {"org/repo"},
		"gitlab_repo":         {"group/sub/repo"},
		"pulumi_backend_url":  {"s3://bucket/state"},
		"vulnetix_org_id":     {"org-1"},
		"onepassword_account": {"my.1password.com"},
	}
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
}

func TestWarningsNameATypo(t *testing.T) {
	w := Warnings(facts("aws_role_arns", roleARN, "aws_log_groups", "/x", "environment", "p", "owner_x", "y", "terraform_dirr", "x"))
	if len(w) != 2 {
		t.Fatalf("warnings = %v", w)
	}
	if !strings.Contains(w[0], `"aws_role_arn"`) || !strings.Contains(w[1], `"terraform_dir"`) {
		t.Errorf("hints = %v", w)
	}
	if got := Warnings(facts("aws_role_arn", roleARN, "tf_var_x", "1")); len(got) != 0 {
		t.Errorf("well-known keys warned: %v", got)
	}
}

func TestHiddenKeysStayOutOfThePrompt(t *testing.T) {
	lines := Visible(facts("aws_external_id", "secret-ish", "environment", "prod", "aws_region", "us-east-1"))
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "secret-ish") || strings.Contains(joined, "aws_external_id") {
		t.Errorf("hidden fact visible: %q", joined)
	}
	if len(lines) != 2 || lines[0] != "aws_region: us-east-1" {
		t.Errorf("lines = %q", lines)
	}
}

func TestTableInvariants(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range table {
		if seen[e.Key] {
			t.Errorf("duplicate key %q", e.Key)
		}
		seen[e.Key] = true
		if !ValidKey(strings.TrimSuffix(e.Key, "_") + "x") {
			t.Errorf("key %q is not a valid fact key", e.Key)
		}
		if e.Value == "" || e.Effect == "" {
			t.Errorf("%q has no documentation text", e.Key)
		}
		if !e.Family && e.Check == nil {
			t.Errorf("%q has no shape check", e.Key)
		}
		for _, s := range secretSuffixes {
			if strings.HasSuffix(e.Key, s) {
				t.Errorf("%q names a secret", e.Key)
			}
		}
		for _, b := range e.Env {
			for _, n := range b.Names {
				if scrubbed(n) {
					t.Errorf("%q binds %s, which the environment scrub strips", e.Key, n)
				}
			}
		}
	}
	for _, c := range Conventional {
		if seen[c] {
			t.Errorf("%q is both well-known and conventional", c)
		}
	}
}

// scrubbed reports whether proc.ScrubbedEnv would drop a variable of this name.
func scrubbed(name string) bool {
	for _, e := range proc.ScrubEnvOf([]string{name + "=x"}) {
		if strings.HasPrefix(e, name+"=") {
			return false
		}
	}
	return true
}

func TestParseRoleARN(t *testing.T) {
	p, a, n, ok := ParseRoleARN(roleARN)
	if !ok || p != "aws" || a != "123456789012" || n != "ReadOnly" {
		t.Errorf("got %q %q %q %v", p, a, n, ok)
	}
	for _, bad := range []string{"", "arn:aws:iam::123:role/x", "arn:aws:iam::123456789012:user/x", "arn:aws:iam::123456789012:role/", roleARN + " "} {
		if _, _, _, ok := ParseRoleARN(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestBindEnvAndFlags(t *testing.T) {
	f := map[string][]string{
		"aws_region":         {"eu-west-2"},
		"terraform_dir":      {"infra"},
		"tf_var_environment": {"prod"},
		"tf_var_zones":       {"a", "b"},
		"kubectl_context":    {"prod"},
		"kubectl_namespace":  {"web"},
		"azure_subscription": {"sub1"},
		"gcloud_project":     {"my-project-123"},
	}
	tf, err := Bind(ToolTerraform, f, []string{"plan"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tf.Front, " "); got != "-chdir=infra" {
		t.Errorf("terraform front = %q", got)
	}
	want := []string{"AWS_REGION=eu-west-2", "AWS_DEFAULT_REGION=eu-west-2", "TF_VAR_environment=prod", `TF_VAR_zones=["a","b"]`}
	for _, w := range want {
		if !contains(tf.Env, w) {
			t.Errorf("terraform env %v missing %q", tf.Env, w)
		}
	}
	if contains(tf.Env, "CLOUDSDK_CORE_PROJECT=my-project-123") || !contains(tf.Env, "GOOGLE_PROJECT=my-project-123") {
		t.Errorf("gcloud_project bound to the wrong tools: %v", tf.Env)
	}

	k, _ := Bind(ToolKubectl, f, []string{"get", "pods"})
	if got := strings.Join(k.Back, " "); got != "--context prod --namespace web" {
		t.Errorf("kubectl back = %q", got)
	}
	if k2, _ := Bind(ToolKubectl, f, []string{"get", "pods", "-A"}); strings.Contains(strings.Join(k2.Back, " "), "--namespace") {
		t.Errorf("-A still got a namespace: %v", k2.Back)
	}
	if k3, _ := Bind(ToolKubectl, f, []string{"config", "view"}); strings.Contains(strings.Join(k3.Back, " "), "--namespace") {
		t.Errorf("config got a namespace: %v", k3.Back)
	}

	az, _ := Bind(ToolAZ, f, []string{"vm", "list"})
	if strings.Join(az.Back, " ") != "--subscription sub1" {
		t.Errorf("az back = %v", az.Back)
	}
	if az2, _ := Bind(ToolAZ, f, []string{"account", "list"}); len(az2.Back) != 0 {
		t.Errorf("account list got %v", az2.Back)
	}
	if gc, _ := Bind(ToolGCloud, f, []string{"projects", "list"}); !contains(gc.Env, "CLOUDSDK_CORE_PROJECT=my-project-123") {
		t.Errorf("gcloud env = %v", gc.Env)
	}
	if other, _ := Bind(ToolGH, f, []string{"pr", "list"}); len(other.Env) != 0 || len(other.Front)+len(other.Back) != 0 {
		t.Errorf("facts leaked to GH: %+v", other)
	}
}

func TestBindExpandsHome(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	a, err := Bind(ToolKubectl, facts("kubectl_kubeconfig", "~/.kube/config"), []string{"get", "pods"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(a.Env, "KUBECONFIG=/home/someone/.kube/config") {
		t.Errorf("env = %v", a.Env)
	}
}

func TestBindRefusesPinnedAndAlwaysRefusedFlags(t *testing.T) {
	pinned := map[string][]string{
		"terraform_dir":   {"infra"},
		"kubectl_context": {"prod"},
		"gcloud_project":  {"my-project-123"},
	}
	refuse := []struct {
		tool  string
		facts map[string][]string
		argv  []string
	}{
		{ToolTerraform, pinned, []string{"-chdir=other", "plan"}},
		{ToolTerraform, pinned, []string{"--chdir", "other", "plan"}},
		{ToolKubectl, pinned, []string{"get", "pods", "--context=other"}},
		{ToolKubectl, pinned, []string{"get", "pods", "--cont", "other"}},
		{ToolGCloud, pinned, []string{"projects", "list", "--project", "other"}},
		{ToolAWS, nil, []string{"s3", "ls", "--profile", "other"}},
		{ToolAWS, nil, []string{"s3", "ls", "--endpoint-url=https://evil.example"}},
		{ToolAWS, nil, []string{"s3", "ls", "--endpoint", "https://evil.example"}},
		{ToolKubectl, nil, []string{"get", "pods", "--server", "https://evil.example"}},
		{ToolKubectl, nil, []string{"get", "pods", "-shttps://evil.example"}},
		{ToolKubectl, nil, []string{"get", "pods", "--token=abc"}},
		{ToolKubectl, nil, []string{"get", "pods", "--as", "admin"}},
		{ToolGCloud, nil, []string{"projects", "list", "--impersonate-service-account=x@y.iam"}},
	}
	for _, c := range refuse {
		if _, err := Bind(c.tool, c.facts, c.argv); err == nil {
			t.Errorf("%s %v: accepted", c.tool, c.argv)
		}
	}
	allow := []struct {
		tool  string
		facts map[string][]string
		argv  []string
	}{
		{ToolTerraform, nil, []string{"-chdir=other", "plan"}}, // not pinned without the fact
		{ToolKubectl, nil, []string{"get", "pods", "--context", "other"}},
		{ToolKubectl, pinned, []string{"get", "pods", "--selector", "app=x", "--sort-by=.metadata.name"}},
		{ToolKubectl, nil, []string{"logs", "pod", "--since=1h", "--tail", "50"}},
		{ToolAWS, nil, []string{"logs", "filter-log-events", "--log-group-name", "/x", "--query", "events", "--output", "json", "--region", "eu-west-1", "--no-paginate"}},
		{ToolGCloud, nil, []string{"projects", "list", "--project", "other"}},
	}
	for _, c := range allow {
		if _, err := Bind(c.tool, c.facts, c.argv); err != nil {
			t.Errorf("%s %v: %v", c.tool, c.argv, err)
		}
	}
	// A flag after -- is an argument to something else.
	if _, err := Bind(ToolKubectl, nil, []string{"get", "pods", "--", "--server"}); err != nil {
		t.Errorf("flag after --: %v", err)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
