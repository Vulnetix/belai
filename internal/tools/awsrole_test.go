package tools

import (
	"context"
	"github.com/vulnetix/belai/internal/repoindex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testRole     = "arn:aws:iam::123456789012:role/ReadOnly"
	otherRole    = "arn:aws:iam::123456789012:role/Admin"
	secretKey    = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	sessionToken = "FwoGZXIvYXdzEXAMPLETOKENVALUE1234567890"
)

// fakeAWS puts an aws and a terraform script on PATH. The aws script logs its
// arguments and the credential variables it sees, answers sts calls from the
// environment and prints the secret key from a logs call (to prove redaction).
type fakeAWS struct {
	log string
	dir string
}

func newFakeAWS(t *testing.T) *fakeAWS {
	t.Helper()
	dir := t.TempDir()
	f := &fakeAWS{log: filepath.Join(dir, "log"), dir: dir}
	aws := `#!/bin/sh
echo "ARGS $*" >> "$FAKE_LOG"
echo "ENV key=$AWS_ACCESS_KEY_ID token=$AWS_SESSION_TOKEN region=$AWS_REGION profile=$AWS_PROFILE" >> "$FAKE_LOG"
case "$1 $2" in
"sts get-caller-identity")
  echo "{\"Account\":\"${FAKE_ACCOUNT:-111122223333}\",\"Arn\":\"${FAKE_ARN:-arn:aws:iam::111122223333:user/dev}\",\"UserId\":\"X\"}" ;;
"sts assume-role")
  if [ -n "$FAKE_DENY" ]; then
    echo "An error occurred (AccessDenied) when calling the AssumeRole operation: User: arn:aws:iam::111122223333:user/dev is not authorized" >&2
    exit 254
  fi
  echo "{\"Credentials\":{\"AccessKeyId\":\"ASIAEXAMPLEKEYID1234\",\"SecretAccessKey\":\"` + secretKey + `\",\"SessionToken\":\"` + sessionToken + `\",\"Expiration\":\"${FAKE_EXPIRY:-2099-01-01T00:00:00Z}\"}}" ;;
*)
  echo "events: hello"
  echo "leak: $AWS_SECRET_ACCESS_KEY $AWS_SESSION_TOKEN" ;;
esac
`
	tf := `#!/bin/sh
echo "TFARGS $*" >> "$FAKE_LOG"
[ -n "$AWS_SESSION_TOKEN" ] && echo creds=yes
echo "workspace=$TF_WORKSPACE var=$TF_VAR_environment region=$AWS_REGION"
`
	for name, body := range map[string]string{"aws": aws, "terraform": tf} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", f.log)
	for _, k := range []string{"FAKE_DENY", "FAKE_ARN", "FAKE_ACCOUNT", "FAKE_EXPIRY", "AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_REGION"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return f
}

func (f *fakeAWS) lines(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(f.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (f *fakeAWS) count(t *testing.T, sub string) int {
	n := 0
	for _, l := range f.lines(t) {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

func nativeNamed(t *testing.T, name string, hub *CloudHub) *Native {
	t.Helper()
	for _, c := range append(cloudCatalog(), localCatalog()...) {
		if c.name == name {
			return &Native{Root: t.TempDir(), Timeout: 30 * time.Second, cmd: c, Cloud: hub}
		}
	}
	t.Fatalf("no native tool %q", name)
	return nil
}

func hubWith(facts map[string][]string) *CloudHub {
	h := &CloudHub{}
	h.SetFacts(facts)
	return h
}

func logsCall(extra string) map[string]any {
	return map[string]any{"command": "logs filter-log-events --log-group-name /app/api" + extra}
}

func TestAWSAssumesTheDeclaredRoleOnceAndHidesTheCredentials(t *testing.T) {
	f := newFakeAWS(t)
	hub := hubWith(map[string][]string{"aws_role_arn": {testRole}, "aws_region": {"eu-west-2"}})
	n := nativeNamed(t, "AWS", hub)

	res, err := n.Execute(context.Background(), logsCall(""))
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindRemote {
		t.Errorf("AWS output holds application text and must classify; kind = %s", res.Kind)
	}
	if !strings.HasPrefix(res.Content, "[harness: assumed role ReadOnly in account 123456789012;") {
		t.Errorf("no harness note:\n%s", res.Content)
	}
	for _, secret := range []string{secretKey, sessionToken} {
		if strings.Contains(res.Content, secret) {
			t.Errorf("a credential reached the result:\n%s", res.Content)
		}
	}
	if !strings.Contains(res.Content, "[redacted]") || !strings.Contains(res.Content, "events: hello") {
		t.Errorf("output should be redacted but kept:\n%s", res.Content)
	}
	log := strings.Join(f.lines(t), "\n")
	if !strings.Contains(log, "key=ASIAEXAMPLEKEYID1234 token="+sessionToken+" region=eu-west-2") {
		t.Errorf("the command did not run with the assumed credentials and the region fact:\n%s", log)
	}
	if !strings.Contains(log, "sts assume-role --role-arn "+testRole+" --role-session-name belai-") || !strings.Contains(log, "--duration-seconds 3600") {
		t.Errorf("assume-role call:\n%s", log)
	}

	// A second call reuses the held credentials and says nothing.
	res, err = n.Execute(context.Background(), logsCall(""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "[harness:") {
		t.Errorf("a reused role was announced again:\n%s", res.Content)
	}
	if got := f.count(t, "sts assume-role"); got != 1 {
		t.Errorf("assume-role ran %d times, want 1", got)
	}
}

func TestAWSRenewsCredentialsNearExpiry(t *testing.T) {
	f := newFakeAWS(t)
	t.Setenv("FAKE_EXPIRY", time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
	n := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_role_arn": {testRole}}))
	for i := 0; i < 2; i++ {
		if _, err := n.Execute(context.Background(), logsCall("")); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.count(t, "sts assume-role"); got != 2 {
		t.Errorf("credentials with a minute left should be renewed; assume-role ran %d times", got)
	}
}

func TestAWSDoesNotAssumeARoleThatIsAlreadyInUse(t *testing.T) {
	f := newFakeAWS(t)
	t.Setenv("FAKE_ARN", "arn:aws:sts::123456789012:assumed-role/ReadOnly/someone")
	t.Setenv("FAKE_ACCOUNT", "123456789012")
	// The role's path is not part of an assumed-role ARN.
	n := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_role_arn": {"arn:aws:iam::123456789012:role/ops/ReadOnly"}}))
	res, err := n.Execute(context.Background(), logsCall(""))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.count(t, "sts assume-role"); got != 0 {
		t.Errorf("assume-role ran %d times for a role already in use", got)
	}
	if strings.Contains(res.Content, "[harness:") {
		t.Errorf("nothing was assumed, so nothing should be announced:\n%s", res.Content)
	}
}

func TestAWSAssumeFailureShowsOnlyTheErrorCode(t *testing.T) {
	newFakeAWS(t)
	t.Setenv("FAKE_DENY", "1")
	n := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_role_arn": {testRole}}))
	_, err := n.Execute(context.Background(), logsCall(""))
	if err == nil {
		t.Fatal("a denied assumption must fail the call")
	}
	if !strings.Contains(err.Error(), "AccessDenied") || !strings.Contains(err.Error(), "ReadOnly") {
		t.Errorf("error = %v", err)
	}
	if strings.Contains(err.Error(), "not authorized") || strings.Contains(err.Error(), "111122223333") {
		t.Errorf("provider text reached the error: %v", err)
	}
}

func TestAWSAsksForARoleThePofileDidNotDeclare(t *testing.T) {
	declared := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_role_arn": {testRole}}))
	none := nativeNamed(t, "AWS", hubWith(nil))
	cases := []struct {
		name string
		n    *Native
		args map[string]any
		want bool
	}{
		{"no role argument", declared, map[string]any{"command": "s3 ls"}, false},
		{"declared role", declared, map[string]any{"command": "s3 ls", "role_arn": testRole}, false},
		{"another role", declared, map[string]any{"command": "s3 ls", "role_arn": otherRole}, true},
		{"no facts at all", none, map[string]any{"command": "s3 ls", "role_arn": testRole}, true},
		{"not an ARN is refused, not asked", declared, map[string]any{"command": "s3 ls", "role_arn": "Admin"}, false},
	}
	for _, c := range cases {
		if got := AlwaysAsksCall(c.n, c.args); got != c.want {
			t.Errorf("%s: asks = %v, want %v", c.name, got, c.want)
		}
	}
	if got := declared.Subject(map[string]any{"command": "s3 ls", "role_arn": otherRole}); got != "s3 ls role="+otherRole {
		t.Errorf("subject = %q", got)
	}
	if got := declared.Subject(map[string]any{"command": "s3 ls"}); got != "s3 ls" {
		t.Errorf("subject = %q", got)
	}
}

func TestAWSRefusesAnInvalidRole(t *testing.T) {
	newFakeAWS(t)
	n := nativeNamed(t, "AWS", hubWith(nil))
	for _, bad := range []string{"Admin", "arn:aws:iam::123:role/x", "arn:aws:iam::123456789012:user/x", "arn:aws:s3:::bucket"} {
		if _, err := n.Execute(context.Background(), map[string]any{"command": "s3 ls", "role_arn": bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestAWSAccountGuard(t *testing.T) {
	f := newFakeAWS(t)
	wrong := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_role_arn": {testRole}, "aws_account_id": {"999999999999"}}))
	if _, err := wrong.Execute(context.Background(), logsCall("")); err == nil || !strings.Contains(err.Error(), "123456789012") {
		t.Errorf("a role in an unlisted account must be refused, got %v", err)
	}
	if f.count(t, "logs filter-log-events") != 0 {
		t.Error("the command ran despite the account guard")
	}
	ok := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_role_arn": {testRole}, "aws_account_id": {"999999999999", "123456789012"}}))
	if _, err := ok.Execute(context.Background(), logsCall("")); err != nil {
		t.Errorf("a listed account: %v", err)
	}

	// With no role the guard checks the caller's own account.
	t.Setenv("FAKE_ACCOUNT", "111122223333")
	own := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_account_id": {"123456789012"}}))
	if _, err := own.Execute(context.Background(), logsCall("")); err == nil {
		t.Error("the caller's account is not listed, the call must be refused")
	}
}

func TestAWSRefusesFlagsThatRedirectCredentials(t *testing.T) {
	newFakeAWS(t)
	n := nativeNamed(t, "AWS", hubWith(nil))
	for _, cmd := range []string{
		"s3 ls --profile other",
		"s3 ls --endpoint-url https://evil.example",
		"s3 ls --endpoint-url=https://evil.example",
		"s3 ls --ca-bundle /tmp/x",
		"s3 ls --no-verify-ssl",
		"logs tail /app/api --follow",
	} {
		if _, err := n.Execute(context.Background(), map[string]any{"command": cmd}); err == nil {
			t.Errorf("%q accepted", cmd)
		}
	}
	for _, cmd := range []string{
		"logs tail /app/api --since 1h",
		"logs start-query --log-group-name /x --start-time 1 --end-time 2 --query-string fields",
		"logs get-query-results --query-id abc",
		"cloudwatch get-metric-data --metric-data-queries file://q.json --start-time 1 --end-time 2",
		"logs filter-log-events --log-group-name /x --filter-pattern ERROR --region eu-west-1 --no-paginate",
	} {
		if _, err := n.Execute(context.Background(), map[string]any{"command": cmd}); err != nil {
			t.Errorf("%q: %v", cmd, err)
		}
	}
}

func TestAWSLogReadsAreReadOnlyPrefixes(t *testing.T) {
	spec := cloudSpecs[1]
	if spec.name != "AWS" {
		t.Fatalf("cloudSpecs[1] = %s", spec.name)
	}
	for _, ok := range []string{"logs filter-log-events", "logs tail", "logs get-log-events", "logs describe-log-streams", "cloudwatch get-metric-data", "cloudwatch describe-alarms"} {
		if !cloudAllowed(spec, ok+" --x y") {
			t.Errorf("%q should be allowed", ok)
		}
	}
	for _, no := range []string{"logs delete-log-group", "logs put-log-events", "logs put-retention-policy", "cloudwatch put-metric-data", "cloudwatch delete-alarms", "sts assume-role", "logs create-log-group"} {
		if cloudAllowed(spec, no+" --x y") {
			t.Errorf("%q must not be allowed", no)
		}
	}
}

func TestTerraformUsesTheDeclaredRoleAndTheFacts(t *testing.T) {
	f := newFakeAWS(t)
	hub := hubWith(map[string][]string{
		"aws_role_arn":        {testRole},
		"aws_region":          {"eu-west-2"},
		"terraform_dir":       {"infra"},
		"terraform_workspace": {"prod"},
		"tf_var_environment":  {"prod"},
	})
	n := nativeNamed(t, "Terraform", hub)
	res, err := n.Execute(context.Background(), map[string]any{"command": "plan"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"creds=yes", "workspace=prod", "var=prod", "region=eu-west-2"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("terraform output lacks %q:\n%s", want, res.Content)
		}
	}
	if !strings.Contains(strings.Join(f.lines(t), "\n"), "TFARGS -chdir=infra plan") {
		t.Errorf("terraform did not run with -chdir:\n%s", strings.Join(f.lines(t), "\n"))
	}
	if _, err := n.Execute(context.Background(), map[string]any{"command": "plan -chdir=other"}); err == nil {
		// The command prefix check passes (the prefix is plan); the pin refuses.
		t.Error("a -chdir flag must be refused while terraform_dir is set")
	}
}

func TestTerraformWithoutFactsRunsAsBefore(t *testing.T) {
	f := newFakeAWS(t)
	n := nativeNamed(t, "Terraform", nil)
	res, err := n.Execute(context.Background(), map[string]any{"command": "validate"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "creds=yes") {
		t.Errorf("credentials without a declared role:\n%s", res.Content)
	}
	for _, l := range f.lines(t) {
		if strings.Contains(l, "-chdir") || strings.HasPrefix(l, "ARGS ") {
			t.Errorf("no facts, no aws calls and no -chdir:\n%s", strings.Join(f.lines(t), "\n"))
			break
		}
	}
}

func TestAssumedCredentialsNeverReachTheEnvTool(t *testing.T) {
	newFakeAWS(t)
	hub := hubWith(map[string][]string{"aws_role_arn": {testRole}})
	if _, err := nativeNamed(t, "AWS", hub).Execute(context.Background(), logsCall("")); err != nil {
		t.Fatal(err)
	}
	res, err := nativeNamed(t, "Env", hub).Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secretKey, sessionToken, "ASIAEXAMPLEKEYID1234"} {
		if strings.Contains(res.Content, secret) {
			t.Errorf("the Env tool printed a credential")
		}
	}
}

func TestNewFactsDropAHeldRole(t *testing.T) {
	f := newFakeAWS(t)
	hub := hubWith(map[string][]string{"aws_role_arn": {testRole}})
	n := nativeNamed(t, "AWS", hub)
	if _, err := n.Execute(context.Background(), logsCall("")); err != nil {
		t.Fatal(err)
	}
	hub.SetFacts(map[string][]string{"aws_role_arn": {testRole}})
	if _, err := n.Execute(context.Background(), logsCall("")); err != nil {
		t.Fatal(err)
	}
	if got := f.count(t, "sts assume-role"); got != 2 {
		t.Errorf("changed facts must not inherit credentials; assume-role ran %d times", got)
	}
}

func TestAWSRoleArgumentPicksAmongDeclaredRoles(t *testing.T) {
	f := newFakeAWS(t)
	hub := hubWith(map[string][]string{"aws_role_arn": {testRole, otherRole}})
	n := nativeNamed(t, "AWS", hub)
	// Two declared roles and no argument: no default, so the ambient identity.
	if _, err := n.Execute(context.Background(), logsCall("")); err != nil {
		t.Fatal(err)
	}
	if f.count(t, "sts assume-role") != 0 {
		t.Error("two declared roles are not a default")
	}
	args := logsCall("")
	args["role_arn"] = otherRole
	if _, err := n.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.lines(t), "\n"), "--role-arn "+otherRole) {
		t.Error("the named role was not assumed")
	}
}

func TestFactsBindToOtherCloudTools(t *testing.T) {
	// GH reads github_repo as GH_REPO; the fake is a shell script printing it.
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"repo=$GH_REPO host=$GH_HOST args=$*\"\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	n := nativeNamed(t, "GH", hubWith(map[string][]string{"github_repo": {"org/repo"}, "github_host": {"ghe.example.com"}}))
	res, err := n.Execute(context.Background(), map[string]any{"command": "pr list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "repo=org/repo host=ghe.example.com args=pr list") {
		t.Errorf("gh output:\n%s", res.Content)
	}
}

// TestCloudHubSurvivesEveryNarrowing pins that the facts a session installs
// reach the AWS tool however the registry was narrowed afterwards, and that a
// registry built without facts has none.
func TestCloudHubSurvivesEveryNarrowing(t *testing.T) {
	newFakeAWS(t)
	caps := Capabilities{cloud: map[string]bool{"AWS": true, "Terraform": true}}
	reg := DefaultWithCaps(t.TempDir(), false, caps, repoindex.Index{})
	if reg.CloudHub() == nil {
		t.Fatal("a default registry has a cloud hub")
	}
	if got := reg.CloudHub().Facts(); got != nil {
		t.Fatalf("no profile, no facts; got %v", got)
	}
	reg.CloudHub().SetFacts(map[string][]string{"aws_role_arn": {testRole}})

	for name, r := range map[string]*Registry{
		"only":     reg.Only("AWS", "Terraform"),
		"readonly": reg.ReadOnly(),
		"surface":  reg.ReadOnlySurface(),
		"plan":     reg.Plan(),
		"with":     reg.With(),
		"kanban":   reg.NarrowWithKanban([]string{"AWS"}, nil, nil),
	} {
		if r.CloudHub() != reg.CloudHub() {
			t.Errorf("%s: a narrowed registry lost the cloud hub", name)
		}
		aws, ok := r.Find("AWS")
		if !ok {
			t.Errorf("%s: no AWS tool", name)
			continue
		}
		if n, _ := aws.(*Native); n == nil || n.Cloud != reg.CloudHub() {
			t.Errorf("%s: the AWS tool does not hold the session's hub", name)
		}
	}
	aws, _ := reg.Only("AWS").Find("AWS")
	res, err := aws.Execute(context.Background(), logsCall(""))
	if err != nil || !strings.Contains(res.Content, "assumed role ReadOnly") {
		t.Fatalf("facts installed on the registry did not reach the tool: %v\n%s", err, res.Content)
	}
}
