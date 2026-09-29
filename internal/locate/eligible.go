package locate

import (
	"path"
	"strings"
)

// Eligibility is decided before anything is ranked or sent: a file that fails
// it is not in the inventory, so it can never be rated, previewed or named.
// The rules are a floor, not a setting.

// Skip reasons, as counted in Inventory.Skipped.
const (
	SkipDependency = "dependency"
	SkipSensitive  = "sensitive"
	SkipHidden     = "hidden"
	SkipSymlink    = "symlink"
	SkipSpecial    = "special"
	SkipIgnored    = "ignored"
	SkipLarge      = "large"
	SkipBinary     = "binary"
	SkipLimit      = "limit"
)

// Inventory caps.
const (
	// MaxFiles is the most files an inventory holds.
	MaxFiles = 100_000
	// MaxFileBytes is the largest file that is eligible.
	MaxFileBytes = 16 << 20
	// sniffBytes is how much of a file is read to tell text from binary.
	sniffBytes = 512
)

// dependencyDirs are package and build output directories.
var dependencyDirs = map[string]bool{
	"node_modules": true, "vendor": true, "venv": true, ".venv": true, ".tox": true,
	"__pycache__": true, "dist": true, "build": true, "coverage": true, "target": true,
	".next": true, ".nuxt": true, ".turbo": true,
}

// sensitiveNames are file names (lower case, exact) that hold credentials.
var sensitiveNames = map[string]bool{
	".netrc": true, ".npmrc": true, ".pypirc": true, ".htpasswd": true, ".pgpass": true,
}

// sensitiveSuffixes are extensions of key and certificate stores.
var sensitiveSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".ppk"}

// binaryExts are extensions that never hold text worth ranking.
var binaryExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".ico": true, ".bmp": true,
	".pdf": true, ".zip": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".7z": true, ".tar": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true, ".o": true, ".class": true, ".jar": true,
	".wasm": true, ".bin": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp3": true, ".mp4": true, ".mov": true, ".avi": true, ".sqlite": true, ".db": true,
}

// skipDir reports whether a directory name is never entered, and why.
func skipDir(name string) (string, bool) {
	if name == ".git" || strings.HasPrefix(name, ".") && name != "." {
		return SkipHidden, true
	}
	if dependencyDirs[strings.ToLower(name)] {
		return SkipDependency, true
	}
	return "", false
}

// skipFile reports whether a file name is not eligible by name alone, and why.
func skipFile(name string) (string, bool) {
	low := strings.ToLower(name)
	if strings.HasPrefix(name, ".") {
		// .env files and other dotfiles: hidden, and the .env family is also
		// named as sensitive so the reason reads right.
		if low == ".env" || strings.HasPrefix(low, ".env.") {
			return SkipSensitive, true
		}
		if sensitiveNames[low] {
			return SkipSensitive, true
		}
		return SkipHidden, true
	}
	if sensitiveNames[low] || strings.HasPrefix(low, "credentials") || strings.HasPrefix(low, "secrets.") ||
		strings.HasPrefix(low, "id_rsa") || strings.HasPrefix(low, "id_ed25519") || strings.HasPrefix(low, "id_ecdsa") || strings.HasPrefix(low, "id_dsa") {
		return SkipSensitive, true
	}
	for _, s := range sensitiveSuffixes {
		if strings.HasSuffix(low, s) {
			return SkipSensitive, true
		}
	}
	if binaryExts[path.Ext(low)] {
		return SkipBinary, true
	}
	return "", false
}

// IsText reports whether the first bytes of a file look like text: no NUL and
// valid enough UTF-8 that a rune is not cut mid-sequence at the end.
func IsText(head []byte) bool {
	for _, b := range head {
		if b == 0 {
			return false
		}
	}
	return true
}

// Language names a file's language by extension, for a label. It is a fixed
// table; an unknown extension is "text".
func Language(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".go":
		return "Go"
	case ".py":
		return "Python"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "JavaScript"
	case ".rs":
		return "Rust"
	case ".java":
		return "Java"
	case ".kt":
		return "Kotlin"
	case ".rb":
		return "Ruby"
	case ".c", ".h":
		return "C"
	case ".cc", ".cpp", ".hpp":
		return "C++"
	case ".cs":
		return "C#"
	case ".md", ".mdx":
		return "Markdown"
	case ".json":
		return "JSON"
	case ".yaml", ".yml":
		return "YAML"
	case ".toml":
		return "TOML"
	case ".sh":
		return "Shell"
	case ".astro":
		return "Astro"
	case ".html":
		return "HTML"
	case ".css":
		return "CSS"
	}
	return "text"
}
