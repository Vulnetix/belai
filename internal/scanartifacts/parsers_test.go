package scanartifacts

import (
	"context"
	"strings"
	"testing"
)

func TestParseCycloneDXMixedScales(t *testing.T) {
	doc := `{
  "vulnerabilities": [
    {
      "id": "CVE-2024-0001",
      "source": {"name": "NVD"},
      "ratings": [
        {"score": 9.300000190734863, "severity": "critical", "method": "other", "source": {"name": "NVD"}},
        {"score": 0.0021, "severity": "low", "method": "other", "source": {"name": "EPSS"}},
        {"score": 2, "severity": "medium", "method": "other", "source": {"name": "SSVC"}},
        {"score": 0.5, "severity": "high", "method": "other", "source": {"name": "Coalition ESS"}}
      ],
      "properties": [
        {"name": "vulnetix:max-severity", "value": "high"}
      ]
    }
  ]
}`
	c, licenses, keys, err := ParseCycloneDX(context.Background(), strings.NewReader(doc), DefaultMaxBytes)
	if err != nil {
		t.Fatalf("ParseCycloneDX: %v", err)
	}
	if c.Total() != 1 {
		t.Fatalf("counts total = %d, want 1", c.Total())
	}
	if c.High != 1 {
		t.Fatalf("expected high=1 from max-severity, got %+v", c)
	}
	if licenses.Total() != 0 {
		t.Fatalf("expected no license findings, got %+v", licenses)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(keys))
	}
}

func TestParseCycloneDXLicenseExcluded(t *testing.T) {
	doc := `{
  "vulnerabilities": [
    {
      "id": "LICENSE-UNKNOWN-001",
      "source": {"name": "vulnetix-license-analyzer"},
      "properties": [{"name": "vulnetix:license-severity", "value": "high"}]
    }
  ]
}`
	c, licenses, _, err := ParseCycloneDX(context.Background(), strings.NewReader(doc), DefaultMaxBytes)
	if err != nil {
		t.Fatalf("ParseCycloneDX: %v", err)
	}
	if c.Total() != 0 {
		t.Fatalf("license vuln should not count as headline: %+v", c)
	}
	if licenses.High != 1 {
		t.Fatalf("expected license high=1, got %+v", licenses)
	}
}

func TestParseSARIFVulnetixSeverity(t *testing.T) {
	doc := `{
  "runs": [{
    "tool": {"driver": {"rules": []}},
    "results": [
      {"ruleId": "VNX-001", "properties": {"severity": "critical"}},
      {"ruleId": "VNX-002", "properties": {"security-severity": "7.5"}},
      {"ruleId": "VNX-003", "level": "warning"}
    ]
  }]
}`
	c, sup, inf, keys, err := ParseSARIF(context.Background(), strings.NewReader(doc), DefaultMaxBytes)
	if err != nil {
		t.Fatalf("ParseSARIF: %v", err)
	}
	if c.Critical != 1 || c.High != 1 || c.Medium != 1 {
		t.Fatalf("unexpected counts: %+v", c)
	}
	if sup.Total() != 0 || inf != 1 {
		t.Fatalf("suppressed=%+v inferred=%d", sup, inf)
	}
	if len(keys) != 3 {
		t.Fatalf("keys = %d, want 3", len(keys))
	}
}

func TestParseSARIFSuppressed(t *testing.T) {
	doc := `{
  "runs": [{
    "tool": {"driver": {"rules": []}},
    "results": [
      {"ruleId": "VNX-001", "level": "error", "suppressions": [{}]}
    ]
  }]
}`
	c, sup, _, _, err := ParseSARIF(context.Background(), strings.NewReader(doc), DefaultMaxBytes)
	if err != nil {
		t.Fatalf("ParseSARIF: %v", err)
	}
	if c.Total() != 0 || sup.High != 1 {
		t.Fatalf("expected suppressed high, headline counts = %+v, suppressed=%+v", c, sup)
	}
}

func TestParseOpenVEX(t *testing.T) {
	doc := `{
  "statements": [
    {"vulnerability": {"name": "CVE-1"}, "products": [{"@id": "pkg:1"}], "status": "affected", "impact_statement": "Severity: high"},
    {"vulnerability": {"name": "CVE-1"}, "products": [{"@id": "pkg:1"}], "status": "affected", "impact_statement": "Severity: high"},
    {"vulnerability": {"name": "CVE-2"}, "products": [{"@id": "pkg:2"}], "status": "fixed"},
    {"vulnerability": {"name": "CVE-3"}, "products": [{"@id": "pkg:3"}], "status": "under_investigation"},
    {"vulnerability": {"name": "CVE-4"}, "products": [{"@id": "pkg:4"}], "status": "affected", "action": {"status": "risk-accepted: agreed"}}
  ]
}`
	c, inv, risk, keys, err := ParseOpenVEX(context.Background(), strings.NewReader(doc), DefaultMaxBytes)
	if err != nil {
		t.Fatalf("ParseOpenVEX: %v", err)
	}
	if c.High != 1 || c.Total() != 1 {
		t.Fatalf("expected one affected high, got %+v", c)
	}
	if inv != 1 || risk != 1 {
		t.Fatalf("investigating=%d risk=%d", inv, risk)
	}
	if len(keys) != 3 {
		t.Fatalf("dedupe keys = %d, want 3 (affected + investigating + risk)", len(keys))
	}
}
