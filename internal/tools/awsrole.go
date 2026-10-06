package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/factspec"
	"github.com/vulnetix/belai/internal/proc"
)

// This file is the AWS tool's optional role feature. A call may name an IAM
// role (the role_arn argument, or the single aws_role_arn fact). The harness
// checks whether that role is already the caller, assumes it when it is not,
// holds the temporary credentials in memory and hands them to the one
// subprocess that needs them. Nothing here reaches a result, a transcript or
// the model: a failure is reduced to AWS's error code, and the credentials'
// own strings are redacted from the output as a last resort.

const (
	// roleMargin is how long before expiry held credentials are renewed.
	roleMargin = 5 * time.Minute
	// identityTTL is how long a caller identity is trusted for the account
	// guard, so a run of calls does not each pay for an STS round trip.
	identityTTL = 2 * time.Minute
	// stsTimeout bounds one STS call.
	stsTimeout = 20 * time.Second
	// defaultSessionSeconds is an assumed role's lifetime without a fact.
	defaultSessionSeconds = 3600
)

// awsCredentialVars are dropped from the environment before assumed
// credentials go in, so an ambient profile or token cannot take precedence.
var awsCredentialVars = []string{
	"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AWS_SECURITY_TOKEN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN",
}

// cloudCall is what a tool's prepare step returns for one call.
type cloudCall struct {
	// env is the subprocess environment.
	env []string
	// note is one harness-composed line put before the output.
	note string
	// redact lists strings removed from the output.
	redact []string
}

type awsIdentity struct {
	Account string `json:"Account"`
	Arn     string `json:"Arn"`
}

type cachedIdentity struct {
	id awsIdentity
	at time.Time
}

// roleFor returns the role a call asks for: the role_arn argument when given,
// else the one declared role. It returns "" when the call uses the ambient
// identity, and an error for a role_arn that is not an IAM role ARN.
func roleFor(args map[string]any, facts map[string][]string) (string, error) {
	if s, _ := argString(args, "role_arn"); strings.TrimSpace(s) != "" {
		s = strings.TrimSpace(s)
		if _, _, _, ok := factspec.ParseRoleARN(s); !ok {
			return "", errors.New("role_arn is not an IAM role ARN (arn:aws:iam::ACCOUNT:role/NAME)")
		}
		return s, nil
	}
	if declared := facts["aws_role_arn"]; len(declared) == 1 {
		return declared[0], nil
	}
	return "", nil
}

// roleDeclared reports whether the profile's facts list arn.
func roleDeclared(facts map[string][]string, arn string) bool {
	for _, d := range facts["aws_role_arn"] {
		if d == arn {
			return true
		}
	}
	return false
}

// awsAsks reports that a call names a role the profile did not declare, which
// asks the user whatever the rules and the ask gate say. A role the profile
// lists is the profile's own declaration and does not.
func awsAsks(h *CloudHub, args map[string]any) bool {
	s, _ := argString(args, "role_arn")
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if _, _, _, ok := factspec.ParseRoleARN(s); !ok {
		return false // the call is refused before it runs
	}
	return !roleDeclared(h.Facts(), s)
}

// awsPrepare is the AWS tool's prepare step.
func awsPrepare(ctx context.Context, n *Native, args map[string]any, env []string) (cloudCall, error) {
	facts := n.Cloud.Facts()
	arn, err := roleFor(args, facts)
	if err != nil {
		return cloudCall{}, err
	}
	return n.Cloud.prepareAWS(ctx, n, env, facts, arn)
}

// terraformPrepare gives Terraform the declared default role's credentials, so
// a plan runs as the role the profile names. Terraform has no role argument: a
// model cannot choose another through it.
func terraformPrepare(ctx context.Context, n *Native, _ map[string]any, env []string) (cloudCall, error) {
	facts := n.Cloud.Facts()
	arn := ""
	if roles := facts["aws_role_arn"]; len(roles) == 1 {
		arn = roles[0]
	}
	return n.Cloud.prepareAWS(ctx, n, env, facts, arn)
}

// prepareAWS resolves the identity a call runs as and checks the account
// guard. env is the scrubbed environment plus the facts' own variables.
func (h *CloudHub) prepareAWS(ctx context.Context, n *Native, env []string, facts map[string][]string, arn string) (cloudCall, error) {
	call := cloudCall{env: env}
	guard := facts["aws_account_id"]
	if arn == "" {
		if len(guard) == 0 {
			return call, nil
		}
		id, err := h.identity(ctx, n, env)
		if err != nil {
			return cloudCall{}, fmt.Errorf("aws_account_id is set, so the caller's account must be verified: %w", err)
		}
		return call, accountAllowed(guard, id.Account)
	}
	role, note, account, err := h.ensureRole(ctx, n, env, facts, arn)
	if err != nil {
		return cloudCall{}, err
	}
	if role != nil {
		call.env = withCreds(env, role)
		call.redact = []string{role.id, role.secret, role.token}
		account = role.account
	}
	call.note = note
	return call, accountAllowed(guard, account)
}

func accountAllowed(guard []string, account string) error {
	if len(guard) == 0 {
		return nil
	}
	for _, g := range guard {
		if g == account {
			return nil
		}
	}
	return fmt.Errorf("the call would run in AWS account %s, which aws_account_id does not list", account)
}

// ensureRole returns the credentials for arn: held ones with time left, none
// when the role is already the caller (account is then the caller's), or a
// fresh assumption. The note is set only when a role was just assumed.
func (h *CloudHub) ensureRole(ctx context.Context, n *Native, env []string, facts map[string][]string, arn string) (*assumedRole, string, string, error) {
	partition, account, name, _ := factspec.ParseRoleARN(arn)
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.roles[arn]; r != nil && time.Until(r.expires) > roleMargin {
		return r, "", account, nil
	}
	if id, err := h.identityLocked(ctx, n, env); err == nil && roleInUse(id.Arn, partition, account, name, arn) {
		return nil, "", id.Account, nil
	}

	secs := factspec.Seconds(facts, "aws_session_seconds")
	if secs == 0 {
		secs = defaultSessionSeconds
	}
	args := []string{
		"sts", "assume-role", "--role-arn", arn,
		"--role-session-name", "belai-" + randHex(4),
		"--duration-seconds", strconv.Itoa(secs), "--output", "json",
	}
	if ext := factspec.One(facts, "aws_external_id"); ext != "" {
		args = append(args, "--external-id", ext)
	}
	out, err := awsExec(ctx, n, env, args...)
	if err != nil {
		return nil, "", "", fmt.Errorf("could not assume role %s: %w", name, err)
	}
	var resp struct {
		Credentials struct {
			AccessKeyID     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
			SessionToken    string `json:"SessionToken"`
			Expiration      string `json:"Expiration"`
		} `json:"Credentials"`
	}
	c := &resp.Credentials
	if json.Unmarshal(out, &resp) != nil || c.AccessKeyID == "" || c.SecretAccessKey == "" || c.SessionToken == "" {
		return nil, "", "", fmt.Errorf("could not assume role %s: the answer held no credentials", name)
	}
	exp, err := time.Parse(time.RFC3339, c.Expiration)
	if err != nil {
		exp = time.Now().Add(time.Duration(secs) * time.Second)
	}
	r := &assumedRole{arn: arn, account: account, name: name, expires: exp, id: c.AccessKeyID, secret: c.SecretAccessKey, token: c.SessionToken}
	if h.roles == nil {
		h.roles = map[string]*assumedRole{}
	}
	h.roles[arn] = r
	note := fmt.Sprintf("[harness: assumed role %s in account %s; the credentials expire %s and are not shown]",
		name, account, exp.UTC().Format(time.RFC3339))
	return r, note, account, nil
}

// roleInUse reports whether the caller identity is arn itself or a session of
// that role (an assumed-role ARN carries the role's name, never its path).
func roleInUse(callerArn, partition, account, name, arn string) bool {
	if callerArn == arn {
		return true
	}
	return strings.HasPrefix(callerArn, "arn:"+partition+":sts::"+account+":assumed-role/"+name+"/")
}

// identity returns the caller identity for env, from the cache when recent.
func (h *CloudHub) identity(ctx context.Context, n *Native, env []string) (awsIdentity, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.identityLocked(ctx, n, env)
}

func (h *CloudHub) identityLocked(ctx context.Context, n *Native, env []string) (awsIdentity, error) {
	key := identityKey(env)
	if c, ok := h.ids[key]; ok && time.Since(c.at) < identityTTL {
		return c.id, nil
	}
	out, err := awsExec(ctx, n, env, "sts", "get-caller-identity", "--output", "json")
	if err != nil {
		return awsIdentity{}, err
	}
	var id awsIdentity
	if json.Unmarshal(out, &id) != nil || id.Account == "" || id.Arn == "" {
		return awsIdentity{}, errors.New("the caller identity answer was not understood")
	}
	if h.ids == nil {
		h.ids = map[string]cachedIdentity{}
	}
	h.ids[key] = cachedIdentity{id: id, at: time.Now()}
	return id, nil
}

// identityKey names the ambient credentials an environment selects.
func identityKey(env []string) string {
	var parts []string
	for _, e := range env {
		for _, p := range []string{"AWS_PROFILE=", "AWS_ACCESS_KEY_ID="} {
			if strings.HasPrefix(e, p) {
				parts = append(parts, e)
			}
		}
	}
	return strings.Join(parts, "\x00")
}

var awsErrorCode = regexp.MustCompile(`\(([A-Za-z][A-Za-z0-9]{2,63})\)`)

// awsExec runs one aws command for the role machinery and returns its stdout.
// A failure is reduced to AWS's error code or the exit status, never the text.
func awsExec(ctx context.Context, n *Native, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, stsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Dir = baseDir(n.Root, n.Cwd)
	cmd.Env = env
	cmd.WaitDelay = 2 * time.Second
	proc.SetProcessGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &capWriter{b: &stdout, max: 64 * 1024}, &capWriter{b: &stderr, max: 64 * 1024}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("the aws CLI is not installed")
		}
		return nil, errors.New("the aws CLI could not be started")
	}
	err := cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, errors.New("the aws CLI timed out")
	}
	if err != nil {
		if m := awsErrorCode.FindStringSubmatch(stderr.String()); m != nil {
			return nil, fmt.Errorf("AWS error %s", m[1])
		}
		return nil, fmt.Errorf("the aws CLI exited with status %d", exitCode(err))
	}
	return stdout.Bytes(), nil
}

// capWriter discards what is written past max.
type capWriter struct {
	b   *bytes.Buffer
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.b.Len(); room > 0 {
		if len(p) > room {
			w.b.Write(p[:room])
		} else {
			w.b.Write(p)
		}
	}
	return len(p), nil
}

// withCreds returns env with the assumed role's credentials in place of any
// ambient ones.
func withCreds(env []string, r *assumedRole) []string {
	out := dropEnv(env, awsCredentialVars...)
	return append(out,
		"AWS_ACCESS_KEY_ID="+r.id,
		"AWS_SECRET_ACCESS_KEY="+r.secret,
		"AWS_SESSION_TOKEN="+r.token,
	)
}

// dropEnv returns env without the named variables.
func dropEnv(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
outer:
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		for _, n := range names {
			if key == n {
				continue outer
			}
		}
		out = append(out, e)
	}
	return out
}

// redact removes each secret from s.
func redact(s string, secrets []string) string {
	for _, v := range secrets {
		if len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}
