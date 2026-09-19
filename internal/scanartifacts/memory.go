package scanartifacts

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultMaxBytes is the largest artifact parsed by default.
const (
	DefaultMaxBytes = 32 << 20
	HardMaxBytes    = 256 << 20
	MaxYAMLPrefix   = 256 << 10
)

// MemorySummary is the top-level count block from memory.yaml.
type MemorySummary struct {
	Timestamp string `yaml:"timestamp"`
	Packages  int    `yaml:"packages"`
	Vulns     int    `yaml:"vulns"`
	Critical  int    `yaml:"critical"`
	High      int    `yaml:"high"`
	Medium    int    `yaml:"medium"`
	Low       int    `yaml:"low"`
}

// ParseMemorySummary reads only the top-level `last_scan` block. It avoids
// pulling a multi-megabyte history/findings tail into memory.
func ParseMemorySummary(path string) (MemorySummary, error) {
	var zero MemorySummary
	f, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), MaxYAMLPrefix)
	inBlock := false
	var block strings.Builder
	bytesRead := 0
	for sc.Scan() {
		line := sc.Text()
		if !inBlock && strings.HasPrefix(strings.TrimSpace(line), "last_scan:") {
			inBlock = true
			block.WriteString(line)
			block.WriteByte('\n')
			continue
		}
		if inBlock {
			// The top-level block ends when we see the next unindented or
			// less-indented key.
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(trimmed, "#") {
				break
			}
			block.WriteString(line)
			block.WriteByte('\n')
			bytesRead += len(line) + 1
			if bytesRead > MaxYAMLPrefix {
				return zero, fmt.Errorf("memory.yaml last_scan block exceeds %d bytes", MaxYAMLPrefix)
			}
		}
	}
	if !inBlock {
		return zero, fmt.Errorf("memory.yaml missing last_scan block")
	}

	var raw struct {
		LastScan MemorySummary `yaml:"last_scan"`
	}
	if err := yaml.Unmarshal([]byte(block.String()), &raw); err != nil {
		return zero, fmt.Errorf("parse last_scan: %w", err)
	}
	return raw.LastScan, nil
}
