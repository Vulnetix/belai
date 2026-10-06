package factspec

import (
	"net/mail"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Tool names, as the registry names them.
const (
	ToolAWS         = "AWS"
	ToolAZ          = "AZ"
	ToolGCloud      = "GCloud"
	ToolKubectl     = "Kubectl"
	ToolTerraform   = "Terraform"
	ToolPulumi      = "Pulumi"
	ToolHeroku      = "Heroku"
	ToolFly         = "Fly"
	ToolVercel      = "Vercel"
	ToolNetlify     = "Netlify"
	ToolDoctl       = "Doctl"
	ToolGH          = "GH"
	ToolGlab        = "Glab"
	ToolOnePassword = "OnePassword"
	ToolVulnetix    = "Vulnetix"
)

var (
	// RoleARN is an IAM role ARN. The groups are partition, account, path and
	// name.
	RoleARN = regexp.MustCompile(`^arn:(aws|aws-cn|aws-us-gov):iam::([0-9]{12}):role/((?:[A-Za-z0-9+=,.@_-]+/)*)([A-Za-z0-9+=,.@_-]{1,64})$`)

	accountID   = regexp.MustCompile(`^[0-9]{12}$`)
	awsRegion   = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)
	identShape  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`)
	profileName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	externalID  = regexp.MustCompile(`^[A-Za-z0-9+=,.@:/_-]{2,512}$`)
	hostname    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(:[0-9]{1,5})?$`)
	ghRepo      = regexp.MustCompile(`^([A-Za-z0-9.-]+/)?[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
	glRepo      = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+$`)
	gcpProject  = regexp.MustCompile(`^[a-z][a-z0-9:.-]{3,61}$`)
	gcpPlace    = regexp.MustCompile(`^[a-z][a-z0-9-]{2,39}$`)
	uuidShape   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	nameShape   = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,128}$`)
	relPath     = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	absPath     = regexp.MustCompile(`^(/|~/)[A-Za-z0-9._/+@:~-]{0,300}$`)
	backendURL  = regexp.MustCompile(`^(https|file|s3|gs|azblob)://[A-Za-z0-9._~:/?&=%@+,-]{1,300}$`)
)

// ParseRoleARN splits an IAM role ARN. The role's name is its last segment
// (the form an assumed-role identity carries has no path).
func ParseRoleARN(arn string) (partition, account, name string, ok bool) {
	m := RoleARN.FindStringSubmatch(arn)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[4], true
}

func isRoleARN(s string) bool   { _, _, _, ok := ParseRoleARN(s); return ok }
func isAccountID(s string) bool { return accountID.MatchString(s) }
func isRegion(s string) bool    { return awsRegion.MatchString(s) }
func isIdent(s string) bool     { return identShape.MatchString(s) }
func isProfile(s string) bool   { return profileName.MatchString(s) }
func isExternal(s string) bool  { return externalID.MatchString(s) }
func isHost(s string) bool      { return hostname.MatchString(s) }
func isGHRepo(s string) bool    { return ghRepo.MatchString(s) }
func isGLRepo(s string) bool    { return glRepo.MatchString(s) }
func isProject(s string) bool   { return gcpProject.MatchString(s) }
func isPlace(s string) bool     { return gcpPlace.MatchString(s) }
func isUUID(s string) bool      { return uuidShape.MatchString(s) }
func isName(s string) bool      { return nameShape.MatchString(s) }
func isBackend(s string) bool   { return backendURL.MatchString(s) }

func isSeconds(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 900 && n <= 43200
}

func isEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && !strings.ContainsAny(s, " <>")
}

// isRelDir is a directory under the session root: plain characters, relative,
// no parent step, and never an option.
func isRelDir(s string) bool {
	if !relPath.MatchString(s) || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "-") || strings.Contains(s, "//") {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// isHostPath is an absolute path or one under the home directory. It is
// expanded when bound (ExpandHome), since an environment variable is not.
func isHostPath(s string) bool {
	if !absPath.MatchString(s) {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// ExpandHome turns a leading ~/ into the user's home directory.
func ExpandHome(p string) string {
	rest, ok := strings.CutPrefix(p, "~/")
	if !ok {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return strings.TrimRight(home, "/") + "/" + rest
}

func env(tool string, names ...string) []EnvBind { return []EnvBind{{Tool: tool, Names: names}} }

func cat[T any](lists ...[]T) []T {
	var out []T
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// azSubscriptionSubcommands are the az commands that take --subscription.
func azTakesSubscription(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch argv[0] {
	case "group", "vm", "aks", "acr", "resource":
		return true
	case "account":
		return len(argv) > 1 && argv[1] == "show"
	}
	return false
}

func not(f func([]string) bool) func([]string) bool {
	return func(a []string) bool { return !f(a) }
}

func first(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}

var table = []Entry{
	{Key: "aws_role_arn", Many: true, Pin: true, Check: isRoleARN,
		Value:  "list of IAM role ARNs",
		Effect: "The roles the AWS tool may assume without asking; with one value, the default role. Terraform uses the default role's credentials.",
	},
	{Key: "aws_account_id", Many: true, Pin: true, Check: isAccountID,
		Value:  "list of 12-digit account ids",
		Effect: "Guard: the caller identity's account must be listed after the role resolves, or the call is refused.",
	},
	{Key: "aws_region", Check: isRegion,
		Value:  "an AWS region",
		Effect: "Sets AWS_REGION and AWS_DEFAULT_REGION for AWS and Terraform. A --region flag still wins.",
		Env:    cat(env(ToolAWS, "AWS_REGION", "AWS_DEFAULT_REGION"), env(ToolTerraform, "AWS_REGION", "AWS_DEFAULT_REGION")),
	},
	{Key: "aws_profile", Pin: true, Check: isProfile,
		Value:  "a profile name from ~/.aws",
		Effect: "Sets AWS_PROFILE, which is also the identity a role is assumed from. --profile is refused.",
		Env:    cat(env(ToolAWS, "AWS_PROFILE"), env(ToolTerraform, "AWS_PROFILE")),
	},
	{Key: "aws_external_id", Hidden: true, Check: isExternal,
		Value:  "an external id",
		Effect: "Passed as --external-id when a role is assumed. Kept out of the prompt.",
	},
	{Key: "aws_session_seconds", Check: isSeconds,
		Value:  "900 to 43200",
		Effect: "The lifetime of an assumed role session (default 3600).",
	},
	{Key: "terraform_dir", Pin: true, Check: isRelDir,
		Value:     "a relative directory under the session root",
		Effect:    "Terraform runs as terraform -chdir=DIR. A -chdir flag is refused.",
		Flags:     []FlagBind{{Tool: ToolTerraform, Flag: "-chdir", Joined: true, Front: true}},
		Overrides: []Override{{Tool: ToolTerraform, Flags: []string{"-chdir"}}},
	},
	{Key: "terraform_workspace", Pin: true, Check: isName,
		Value:  "a workspace name",
		Effect: "Sets TF_WORKSPACE.",
		Env:    env(ToolTerraform, "TF_WORKSPACE"),
	},
	{Key: "tf_var_", Family: true, Many: true,
		Value:  "a string or a list",
		Effect: "tf_var_NAME sets TF_VAR_NAME for Terraform. A list becomes a JSON array. NAME is lowercase.",
		Env:    []EnvBind{{Tool: ToolTerraform}},
	},
	{Key: "kubectl_context", Pin: true, Check: isName,
		Value:     "a kubeconfig context name",
		Effect:    "Adds --context. A --context flag is refused.",
		Flags:     []FlagBind{{Tool: ToolKubectl, Flag: "--context"}},
		Overrides: []Override{{Tool: ToolKubectl, Flags: []string{"--context"}}},
	},
	{Key: "kubectl_namespace", Check: isName,
		Value:  "a namespace",
		Effect: "Adds --namespace unless the call names one with -n, --namespace or -A.",
		Flags: []FlagBind{{Tool: ToolKubectl, Flag: "--namespace",
			Unless: []string{"-n", "--namespace", "-A", "--all-namespaces"},
			Skip:   func(a []string) bool { return first(a) == "config" }}},
	},
	{Key: "kubectl_kubeconfig", Pin: true, Check: isHostPath,
		Value:     "an absolute path or one under ~/",
		Effect:    "Sets KUBECONFIG. A --kubeconfig flag is refused.",
		Env:       env(ToolKubectl, "KUBECONFIG"),
		Overrides: []Override{{Tool: ToolKubectl, Flags: []string{"--kubeconfig"}}},
	},
	{Key: "azure_subscription", Pin: true, Check: isIdent,
		Value:     "a subscription id or name",
		Effect:    "Adds --subscription to the az commands that take it, and sets ARM_SUBSCRIPTION_ID for Terraform. A --subscription flag is refused.",
		Env:       env(ToolTerraform, "ARM_SUBSCRIPTION_ID"),
		Flags:     []FlagBind{{Tool: ToolAZ, Flag: "--subscription", Skip: not(azTakesSubscription)}},
		Overrides: []Override{{Tool: ToolAZ, Flags: []string{"--subscription"}}},
	},
	{Key: "azure_tenant", Pin: true, Check: isUUID,
		Value:  "a tenant id",
		Effect: "Sets ARM_TENANT_ID for Terraform.",
		Env:    env(ToolTerraform, "ARM_TENANT_ID"),
	},
	{Key: "azure_resource_group", Check: isName,
		Value:  "a resource group name",
		Effect: "Sets AZURE_DEFAULTS_GROUP, the group az uses when a command names none.",
		Env:    env(ToolAZ, "AZURE_DEFAULTS_GROUP"),
	},
	{Key: "azure_location", Check: isName,
		Value:  "an Azure location",
		Effect: "Sets AZURE_DEFAULTS_LOCATION.",
		Env:    env(ToolAZ, "AZURE_DEFAULTS_LOCATION"),
	},
	{Key: "azure_config_dir", Pin: true, Check: isHostPath,
		Value:  "an absolute path or one under ~/",
		Effect: "Sets AZURE_CONFIG_DIR, which holds az's accounts and tokens.",
		Env:    env(ToolAZ, "AZURE_CONFIG_DIR"),
	},
	{Key: "gcloud_project", Pin: true, Check: isProject,
		Value:     "a project id",
		Effect:    "Sets CLOUDSDK_CORE_PROJECT, and GOOGLE_PROJECT for Terraform. A --project flag is refused.",
		Env:       cat(env(ToolGCloud, "CLOUDSDK_CORE_PROJECT"), env(ToolTerraform, "GOOGLE_PROJECT")),
		Overrides: []Override{{Tool: ToolGCloud, Flags: []string{"--project"}}},
	},
	{Key: "gcloud_account", Pin: true, Check: isEmail,
		Value:     "an account email",
		Effect:    "Sets CLOUDSDK_CORE_ACCOUNT. A --account flag is refused.",
		Env:       env(ToolGCloud, "CLOUDSDK_CORE_ACCOUNT"),
		Overrides: []Override{{Tool: ToolGCloud, Flags: []string{"--account"}}},
	},
	{Key: "gcloud_region", Check: isPlace,
		Value:  "a Compute region",
		Effect: "Sets CLOUDSDK_COMPUTE_REGION, and GOOGLE_REGION for Terraform.",
		Env:    cat(env(ToolGCloud, "CLOUDSDK_COMPUTE_REGION"), env(ToolTerraform, "GOOGLE_REGION")),
	},
	{Key: "gcloud_zone", Check: isPlace,
		Value:  "a Compute zone",
		Effect: "Sets CLOUDSDK_COMPUTE_ZONE, and GOOGLE_ZONE for Terraform.",
		Env:    cat(env(ToolGCloud, "CLOUDSDK_COMPUTE_ZONE"), env(ToolTerraform, "GOOGLE_ZONE")),
	},
	{Key: "gcloud_impersonate_service_account", Pin: true, Check: isEmail,
		Value:  "a service account email",
		Effect: "Sets CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT, and GOOGLE_IMPERSONATE_SERVICE_ACCOUNT for Terraform. Only a profile declares it; a --impersonate-service-account flag is refused.",
		Env: cat(env(ToolGCloud, "CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT"),
			env(ToolTerraform, "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT")),
	},
	{Key: "gcloud_configuration", Pin: true, Check: isProfile,
		Value:     "a gcloud configuration name",
		Effect:    "Sets CLOUDSDK_ACTIVE_CONFIG_NAME. A --configuration flag is refused.",
		Env:       env(ToolGCloud, "CLOUDSDK_ACTIVE_CONFIG_NAME"),
		Overrides: []Override{{Tool: ToolGCloud, Flags: []string{"--configuration"}}},
	},
	{Key: "github_host", Check: isHost,
		Value:  "a hostname",
		Effect: "Sets GH_HOST, the host gh uses when a command names none.",
		Env:    env(ToolGH, "GH_HOST"),
	},
	{Key: "github_repo", Check: isGHRepo,
		Value:  "[HOST/]OWNER/REPO",
		Effect: "Sets GH_REPO, the repository gh uses outside a checkout.",
		Env:    env(ToolGH, "GH_REPO"),
	},
	{Key: "gitlab_host", Check: isHost,
		Value:  "a hostname",
		Effect: "Sets GITLAB_HOST.",
		Env:    env(ToolGlab, "GITLAB_HOST"),
	},
	{Key: "gitlab_repo", Check: isGLRepo,
		Value:  "GROUP/REPO",
		Effect: "Sets GITLAB_REPO.",
		Env:    env(ToolGlab, "GITLAB_REPO"),
	},
	{Key: "gitlab_group", Check: isGLRepo,
		Value:  "a group path",
		Effect: "Sets GITLAB_GROUP.",
		Env:    env(ToolGlab, "GITLAB_GROUP"),
	},
	{Key: "pulumi_stack", Check: isName,
		Value:  "a stack name",
		Effect: "Sets PULUMI_STACK.",
		Env:    env(ToolPulumi, "PULUMI_STACK"),
	},
	{Key: "pulumi_backend_url", Pin: true, Check: isBackend,
		Value:  "a URL with scheme https, file, s3, gs or azblob",
		Effect: "Sets PULUMI_BACKEND_URL.",
		Env:    env(ToolPulumi, "PULUMI_BACKEND_URL"),
	},
	{Key: "heroku_app", Check: isName,
		Value:  "an app name",
		Effect: "Sets HEROKU_APP.",
		Env:    env(ToolHeroku, "HEROKU_APP"),
	},
	{Key: "fly_app", Check: isName,
		Value:  "an app name",
		Effect: "Sets FLY_APP.",
		Env:    env(ToolFly, "FLY_APP"),
	},
	{Key: "vercel_org_id", Pin: true, Check: isIdent,
		Value:  "an organization id",
		Effect: "Sets VERCEL_ORG_ID.",
		Env:    env(ToolVercel, "VERCEL_ORG_ID"),
	},
	{Key: "vercel_project_id", Pin: true, Check: isIdent,
		Value:  "a project id",
		Effect: "Sets VERCEL_PROJECT_ID.",
		Env:    env(ToolVercel, "VERCEL_PROJECT_ID"),
	},
	{Key: "vercel_scope", Check: isName,
		Value:  "a team slug",
		Effect: "Adds --scope to the vercel commands that take it.",
		Flags: []FlagBind{{Tool: ToolVercel, Flag: "--scope", Unless: []string{"--scope", "-S"},
			Skip: func(a []string) bool { return first(a) == "whoami" }}},
	},
	{Key: "netlify_site_id", Check: isIdent,
		Value:  "a site id",
		Effect: "Sets NETLIFY_SITE_ID.",
		Env:    env(ToolNetlify, "NETLIFY_SITE_ID"),
	},
	{Key: "doctl_context", Pin: true, Check: isName,
		Value:     "a doctl context name",
		Effect:    "Sets DIGITALOCEAN_CONTEXT. A --context flag is refused.",
		Env:       env(ToolDoctl, "DIGITALOCEAN_CONTEXT"),
		Overrides: []Override{{Tool: ToolDoctl, Flags: []string{"--context"}}},
	},
	{Key: "onepassword_account", Pin: true, Check: isIdent,
		Value:     "an account sign-in address or id",
		Effect:    "Sets OP_ACCOUNT. An --account flag is refused.",
		Env:       env(ToolOnePassword, "OP_ACCOUNT"),
		Overrides: []Override{{Tool: ToolOnePassword, Flags: []string{"--account"}}},
	},
	{Key: "vulnetix_org_id", Check: isIdent,
		Value:  "an organization id",
		Effect: "Sets VULNETIX_ORG_ID for the Vulnetix tool.",
		Env:    env(ToolVulnetix, "VULNETIX_ORG_ID"),
	},
	{Key: "vulnetix_project", Check: isIdent,
		Value:  "a project name",
		Effect: "Sets VULNETIX_PROJECT.",
		Env:    env(ToolVulnetix, "VULNETIX_PROJECT"),
	},
	{Key: "vulnetix_namespace", Check: isIdent,
		Value:  "a namespace",
		Effect: "Sets VULNETIX_NAMESPACE.",
		Env:    env(ToolVulnetix, "VULNETIX_NAMESPACE"),
	},
	{Key: "vulnetix_environment", Check: isIdent,
		Value:  "an environment name",
		Effect: "Sets VULNETIX_ENVIRONMENT.",
		Env:    env(ToolVulnetix, "VULNETIX_ENVIRONMENT"),
	},
}
