// Package build determines how a repository should be compiled.
//
// Build configuration is part of a snapshot's identity, not a detail. Files
// excluded by build tags are invisible to the type checker, so the same commit
// yields different facts under different tags, and a finding is only
// meaningful alongside the configuration that produced it.
package build

import (
    "bufio"
    "fmt"
    "os"
    "path/filepath"
    "strconv"
    "strings"
)

// Config is the configuration a snapshot was analyzed under.
type Config struct {
    Module    string   `json:"module"`
    GoVersion string   `json:"go_version"`
    GOOS      string   `json:"goos"`
    GOARCH    string   `json:"goarch"`
    Tags      []string `json:"tags,omitempty"`
}

// Detect reads go.mod. It defaults to linux/amd64, which is what almost all
// cloud-native Go is actually built for.
func Detect(repoDir string) (Config, error) {
    c := Config{GOOS: "linux", GOARCH: "amd64"}

    f, err := os.Open(filepath.Join(repoDir, "go.mod"))
    if err != nil {
        return c, fmt.Errorf("read go.mod: %w", err)
    }
    defer f.Close()

    sc := bufio.NewScanner(f)
    for sc.Scan() {
        line := strings.TrimSpace(sc.Text())
        switch {
        case strings.HasPrefix(line, "module "):
            c.Module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
        case strings.HasPrefix(line, "go "):
            c.GoVersion = strings.TrimSpace(strings.TrimPrefix(line, "go "))
        case strings.HasPrefix(line, "toolchain ") && c.GoVersion == "":
            v := strings.TrimSpace(strings.TrimPrefix(line, "toolchain "))
            c.GoVersion = strings.TrimPrefix(v, "go")
        }
    }

    return c, sc.Err()
}

// LoopVarPerIteration reports whether each loop iteration gets its own
// variable, which changed in Go 1.22.
//
// This single fact decides whether capturing a loop variable in a goroutine is
// a bug or correct code. The same source, under two different go.mod versions,
// has two different answers — which makes it the cleanest counterfactual pair
// available for the hard-negative dataset.
func (c Config) LoopVarPerIteration() bool { return c.AtLeast(1, 22) }

// TimersGarbageCollected reports whether unstopped timers are collected, which
// changed in Go 1.23. Before it, a leaked Ticker was a real leak.
func (c Config) TimersGarbageCollected() bool { return c.AtLeast(1, 23) }

// GoroutineLeakProfile reports whether the toolchain can produce the
// experimental goroutine leak profile introduced in Go 1.26, which the
// verification ladder uses for dynamic confirmation.
func (c Config) GoroutineLeakProfile() bool { return c.AtLeast(1, 26) }

// AtLeast reports whether the module's declared Go version is at least
// major.minor. An unparseable version returns false: assuming the newer, safer
// semantics would silently turn real bugs into non-findings.
func (c Config) AtLeast(major, minor int) bool {
    gotMajor, gotMinor, ok := parseVersion(c.GoVersion)
    if !ok {
        return false
    }
    if gotMajor != major {
        return gotMajor > major
    }
    return gotMinor >= minor
}

func parseVersion(v string) (major, minor int, ok bool) {
    v = strings.TrimPrefix(strings.TrimSpace(v), "go")
    parts := strings.Split(v, ".")
    if len(parts) < 2 {
        return 0, 0, false
    }

    var err error
    if major, err = strconv.Atoi(parts[0]); err != nil {
        return 0, 0, false
    }
    if minor, err = strconv.Atoi(parts[1]); err != nil {
        return 0, 0, false
    }
    return major, minor, true
}
