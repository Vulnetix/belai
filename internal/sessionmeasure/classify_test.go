package sessionmeasure

import "testing"

func TestClassifyArgv(t *testing.T) {
	cases := []struct {
		argv []string
		want Class
		ok   bool
	}{
		// go
		{[]string{"go", "build", "./..."}, Class{KindBuild, "go", "build"}, true},
		{[]string{"go", "install", "./cmd/belai"}, Class{KindBuild, "go", "install"}, true},
		{[]string{"go", "test", "-count=1", "./..."}, Class{KindTest, "go", "test"}, true},
		{[]string{"/usr/local/go/bin/go", "test", "./..."}, Class{KindTest, "go", "test"}, true},
		{[]string{"go", "vet", "./internal/..."}, Class{KindLint, "go", "vet"}, true},
		{[]string{"go", "run", "./cmd/x"}, Class{}, false},
		{[]string{"go", "mod", "tidy"}, Class{}, false},
		{[]string{"go"}, Class{}, false},
		// cargo
		{[]string{"cargo", "build", "--release"}, Class{KindBuild, "cargo", "build"}, true},
		{[]string{"cargo", "b"}, Class{KindBuild, "cargo", "build"}, true},
		{[]string{"cargo", "check", "-p", "core"}, Class{KindBuild, "cargo", "check"}, true},
		{[]string{"cargo", "+nightly", "test", "--all"}, Class{KindTest, "cargo", "test"}, true},
		{[]string{"cargo", "nextest", "run"}, Class{KindTest, "cargo", "nextest"}, true},
		{[]string{"cargo", "clippy", "--", "-D", "warnings"}, Class{KindLint, "cargo", "clippy"}, true},
		{[]string{"cargo", "fmt", "--check"}, Class{KindLint, "cargo", "fmt"}, true},
		{[]string{"cargo", "fmt"}, Class{}, false},
		{[]string{"cargo", "run"}, Class{}, false},
		// npm, yarn, pnpm
		{[]string{"npm", "test"}, Class{KindTest, "npm", "test"}, true},
		{[]string{"npm", "t"}, Class{KindTest, "npm", "test"}, true},
		{[]string{"npm", "run", "build"}, Class{KindBuild, "npm", "build"}, true},
		{[]string{"npm", "run", "test:unit", "--", "--watch=false"}, Class{KindTest, "npm", "test:unit"}, true},
		{[]string{"npm", "run-script", "lint"}, Class{KindLint, "npm", "lint"}, true},
		{[]string{"npm", "--prefix", "web", "run", "build"}, Class{KindBuild, "npm", "build"}, true},
		{[]string{"npm", "install"}, Class{}, false},
		{[]string{"npm", "run", "dev"}, Class{}, false},
		{[]string{"npm", "run"}, Class{}, false},
		{[]string{"yarn", "test"}, Class{KindTest, "yarn", "test"}, true},
		{[]string{"yarn", "build"}, Class{KindBuild, "yarn", "build"}, true},
		{[]string{"yarn", "lint"}, Class{KindLint, "yarn", "lint"}, true},
		{[]string{"yarn", "workspace", "web", "test"}, Class{KindTest, "yarn", "test"}, true},
		{[]string{"yarn", "add", "left-pad"}, Class{}, false},
		{[]string{"pnpm", "test"}, Class{KindTest, "pnpm", "test"}, true},
		{[]string{"pnpm", "-r", "run", "build"}, Class{KindBuild, "pnpm", "build"}, true},
		{[]string{"pnpm", "--filter", "web", "lint"}, Class{KindLint, "pnpm", "lint"}, true},
		{[]string{"pnpm", "install"}, Class{}, false},
		// make
		{[]string{"make"}, Class{KindBuild, "make", ""}, true},
		{[]string{"make", "build"}, Class{KindBuild, "make", "build"}, true},
		{[]string{"make", "-j8", "all"}, Class{KindBuild, "make", "all"}, true},
		{[]string{"make", "-C", "sub", "test"}, Class{KindTest, "make", "test"}, true},
		{[]string{"make", "test-unit", "CI=1"}, Class{KindTest, "make", "test-unit"}, true},
		{[]string{"make", "check"}, Class{KindTest, "make", "check"}, true},
		{[]string{"make", "lint"}, Class{KindLint, "make", "lint"}, true},
		{[]string{"make", "vet"}, Class{KindLint, "make", "vet"}, true},
		{[]string{"make", "clean"}, Class{}, false},
		{[]string{"make", "CC=clang"}, Class{KindBuild, "make", ""}, true},
		// pytest
		{[]string{"pytest"}, Class{KindTest, "pytest", ""}, true},
		{[]string{"pytest", "-x", "tests/"}, Class{KindTest, "pytest", ""}, true},
		{[]string{"python3", "-m", "pytest", "-q"}, Class{KindTest, "pytest", ""}, true},
		{[]string{"python", "-m", "http.server"}, Class{}, false},
		{[]string{"python", "script.py"}, Class{}, false},
		{[]string{"uv", "run", "pytest"}, Class{KindTest, "pytest", ""}, true},
		{[]string{"poetry", "run", "pytest", "-k", "x"}, Class{KindTest, "pytest", ""}, true},
		{[]string{"uv", "sync"}, Class{}, false},
		// mvn
		{[]string{"mvn", "test"}, Class{KindTest, "mvn", "test"}, true},
		{[]string{"mvn", "-q", "clean", "verify"}, Class{KindTest, "mvn", "verify"}, true},
		{[]string{"./mvnw", "-pl", "core", "package"}, Class{KindBuild, "mvn", "package"}, true},
		{[]string{"mvn", "compile"}, Class{KindBuild, "mvn", "compile"}, true},
		{[]string{"mvn", "clean", "install", "-DskipTests"}, Class{KindBuild, "mvn", "install"}, true},
		{[]string{"mvn", "checkstyle:check"}, Class{KindLint, "mvn", "checkstyle:check"}, true},
		{[]string{"mvn", "clean"}, Class{}, false},
		{[]string{"mvn", "dependency:tree"}, Class{}, false},
		// not checks
		{[]string{"git", "status"}, Class{}, false},
		{[]string{"echo", "go", "test"}, Class{}, false},
		{[]string{"ls"}, Class{}, false},
		{nil, Class{}, false},
	}
	for _, c := range cases {
		got, ok := Classify(c.argv)
		if ok != c.ok || got != c.want {
			t.Errorf("Classify(%q) = %+v, %v; want %+v, %v", c.argv, got, ok, c.want, c.ok)
		}
	}
}

func TestClassifyLine(t *testing.T) {
	cases := []struct {
		line string
		want Class
		ok   bool
	}{
		{"go test ./...", Class{KindTest, "go", "test"}, true},
		{"cd internal/x && go test -run TestY .", Class{KindTest, "go", "test"}, true},
		{"CGO_ENABLED=0 go build -o bin/belai ./cmd/belai", Class{KindBuild, "go", "build"}, true},
		{"go test ./... 2>&1 | tail -20", Class{KindTest, "go", "test"}, true},
		{"time go vet ./...", Class{KindLint, "go", "vet"}, true},
		{"sudo -n make test", Class{KindTest, "make", "test"}, true},
		{"env CI=1 npm run build", Class{KindBuild, "npm", "build"}, true},
		{"sh -c 'cargo test --all'", Class{KindTest, "cargo", "test"}, true},
		// A chain is recorded once, as its last check.
		{"go build ./... && go vet ./... && go test ./...", Class{KindTest, "go", "test"}, true},
		{"go test ./... && echo done", Class{KindTest, "go", "test"}, true},
		{"npm ci && npm run build", Class{KindBuild, "npm", "build"}, true},
		// Mentions are not checks.
		{`echo "go test ./..."`, Class{}, false},
		{"git commit -m 'run go test'", Class{}, false},
		{"grep -r 'make test' docs", Class{}, false},
		{"# go test", Class{}, false},
		{"ls -la", Class{}, false},
		{"", Class{}, false},
		{"go test ./... \"unterminated", Class{}, false},
	}
	for _, c := range cases {
		got, ok := ClassifyLine(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("ClassifyLine(%q) = %+v, %v; want %+v, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}
