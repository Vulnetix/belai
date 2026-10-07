package agentimport

import (
	"fmt"
	"strings"
)

// ParseFormat reads a format name as the command line spells it. The empty
// string and "auto" ask Import to detect the format.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return "", nil
	case "claws", "claw":
		return Claws, nil
	case "nemoclaw", "nvidia", "fabric":
		return Nemoclaw, nil
	case "hermes":
		return Hermes, nil
	case "mini-swe", "miniswe", "mini-swe-agent":
		return MiniSWE, nil
	}
	return "", fmt.Errorf("unknown format %q (want claws, nemoclaw, hermes, mini-swe or auto)", s)
}

// Import reads the definition at path (a file, a directory or a .tar.gz) in the
// given format and converts it. An empty format is detected from the files.
func Import(path string, f Format, o Options) (Result, error) {
	t, err := load(path)
	if err != nil {
		return Result{}, err
	}
	if f == "" {
		if f, err = detect(t); err != nil {
			return Result{}, err
		}
	}
	return importTree(t, f, o)
}

// importTree runs the adapter for one of the four formats Import reads.
func importTree(t *tree, f Format, o Options) (Result, error) {
	switch f {
	case Claws:
		return importClaws(t, o)
	case Nemoclaw:
		return importFabric(t, o)
	case Hermes:
		return importHermes(t, o)
	case MiniSWE:
		return importMiniSWE(t, o)
	}
	return Result{}, fmt.Errorf("unknown format %q", f)
}

// detect names the format of a tree from the files it holds.
func detect(t *tree) (Format, error) {
	switch {
	case findFile(t, "CLAW.md") != "":
		return Claws, nil
	case t.has("SOUL.md") && (t.has("config.yaml") || t.has("profile.yaml") || t.has("distribution.yaml")):
		return Hermes, nil
	case pickYAML(t, isFabricDoc) != "":
		return Nemoclaw, nil
	case pickYAML(t, isMiniSWEDoc) != "":
		return MiniSWE, nil
	}
	return "", fmt.Errorf("cannot tell which format this is; pass -from claws, nemoclaw, hermes or mini-swe")
}
