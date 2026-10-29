package tools

import (
	"strings"
	"testing"
)

func awsSpec(t *testing.T) cloudSpec {
	t.Helper()
	for _, cs := range cloudSpecs {
		if cs.name == "AWS" {
			return cs
		}
	}
	t.Fatal("no AWS cloud spec")
	return cloudSpec{}
}

// The investigation commands (docs/guardian.md) are allowed, and the writes
// that share a service with them are not.
func TestAWSInvestigationPrefixes(t *testing.T) {
	spec := awsSpec(t)
	allowed := []string{
		"cloudtrail lookup-events --lookup-attributes AttributeKey=EventName,AttributeValue=ConsoleLogin",
		"route53resolver list-resolver-rules",
		"route53resolver get-resolver-query-log-config --resolver-query-log-config-id rqlc-1",
		"wafv2 get-web-acl --name a --scope REGIONAL --id 1",
		"wafv2 list-web-acls --scope CLOUDFRONT",
		"guardduty list-findings --detector-id d1",
		"guardduty get-findings --detector-id d1 --finding-ids f1",
		"ecs describe-services --cluster c --services s",
		"ecs list-tasks --cluster c",
		"ssm describe-instance-information",
		"iam get-role --role-name r",
		"iam get-account-authorization-details",
		"iam generate-credential-report",
		"iam get-credential-report",
		"s3api get-bucket-policy --bucket b",
		"s3api get-bucket-acl --bucket b",
		"s3api get-public-access-block --bucket b",
		"configservice describe-compliance-by-resource",
		"elbv2 describe-load-balancers",
		"elbv2 describe-listeners --load-balancer-arn arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/x/1",
		// The commands that were already there still pass.
		"sts get-caller-identity",
		"logs filter-log-events --log-group-name /app/api",
	}
	for _, cmd := range allowed {
		if !cloudAllowed(spec, cmd) {
			t.Errorf("%q is refused, want allowed", cmd)
		}
	}
	refused := []string{
		"cloudtrail stop-logging --name t",
		"cloudtrail delete-trail --name t",
		"route53resolver delete-resolver-rule --resolver-rule-id r",
		"route53resolver associate-resolver-rule --resolver-rule-id r",
		"wafv2 update-web-acl --name a",
		"wafv2 delete-web-acl --name a",
		"guardduty delete-detector --detector-id d1",
		"guardduty archive-findings --detector-id d1",
		"ecs update-service --cluster c --service s",
		"ecs run-task --cluster c",
		"ssm send-command --document-name AWS-RunShellScript",
		"ssm get-parameter --name /prod/db --with-decryption",
		"iam create-access-key --user-name u",
		"iam put-role-policy --role-name r",
		"iam attach-role-policy --role-name r",
		"s3api put-bucket-policy --bucket b",
		"s3api delete-public-access-block --bucket b",
		"s3api get-object-acl --bucket b --key k",
		"configservice delete-config-rule --config-rule-name r",
		"elbv2 modify-listener --listener-arn l",
		"elbv2 delete-load-balancer --load-balancer-arn l",
		// A star stands for the rest of one operation name, never for nothing
		// and never for a word that leaves the family.
		"iam get-",
		"iam get-Role",
		"iam get-role_x",
		"wafv2 get",
		"wafv2get-web-acl",
		"guardduty",
	}
	for _, cmd := range refused {
		if cloudAllowed(spec, cmd) {
			t.Errorf("%q is allowed, want refused", cmd)
		}
	}
}

// The new commands keep the AWS tool's other refusals: credentials pointed at
// another endpoint or identity, and shell syntax.
func TestAWSInvestigationKeepsRefusals(t *testing.T) {
	build := cloudNative(awsSpec(t)).build
	for _, cmd := range []string{
		"cloudtrail lookup-events --profile other",
		"wafv2 list-web-acls --scope REGIONAL --endpoint-url https://evil.example",
		"s3api get-bucket-policy --bucket b --no-sign-request",
		"iam get-role --role-name r; aws iam delete-role --role-name r",
		"guardduty list-findings --detector-id $(id)",
	} {
		if _, _, err := build(t.TempDir(), map[string]any{"command": cmd}); err == nil {
			t.Errorf("%q was accepted", cmd)
		}
	}
	argv, _, err := build(t.TempDir(), map[string]any{"command": "guardduty list-detectors"})
	if err != nil {
		t.Fatalf("guardduty list-detectors: %v", err)
	}
	if strings.Join(argv, " ") != "guardduty list-detectors" {
		t.Fatalf("argv = %v", argv)
	}
	if cloudNative(awsSpec(t)).kind != KindRemote {
		t.Fatal("AWS output must stay classified")
	}
}
