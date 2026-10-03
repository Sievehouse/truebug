// Package ingest fetches repositories and takes inventory of what is in them.
//
// Inventory is deliberately cheap and syntactic: it never builds or executes
// anything. Everything expensive, and everything that runs code, belongs in
// the build and analysis stages behind a sandbox.
package ingest

import (
    "bufio"
    "os"
    "path/filepath"
    "regexp"
    "strings"
)

// FileInfo is one source file as the inventory sees it.
type FileInfo struct {
    Path        string `json:"path"`
    Language    string `json:"language"`
    Lines       int    `json:"lines"`
    Bytes       int64  `json:"bytes"`
    IsTest      bool   `json:"is_test"`
    IsVendored  bool   `json:"is_vendored"`
    IsGenerated bool   `json:"is_generated"`
}

// Inventory is the result of walking a repository.
type Inventory struct {
    Root       string         `json:"root"`
    Files      []FileInfo     `json:"files"`
    ByLanguage map[string]int `json:"by_language"`
    TotalBytes int64          `json:"total_bytes"`
    TotalLines int            `json:"total_lines"`
}

var languageByExt = map[string]string{
    ".go":    "Go",
    ".py":    "Python",
    ".ts":    "TypeScript",
    ".tsx":   "TypeScript",
    ".js":    "JavaScript",
    ".jsx":   "JavaScript",
    ".rs":    "Rust",
    ".c":     "C",
    ".h":     "C",
    ".cc":    "C++",
    ".cpp":   "C++",
    ".hpp":   "C++",
    ".cu":    "CUDA",
    ".cuh":   "CUDA",
    ".java":  "Java",
    ".sh":    "Shell",
    ".yaml":  "YAML",
    ".yml":   "YAML",
    ".json":  "JSON",
    ".proto": "Protobuf",
    ".tf":    "Terraform",
}

// skippedDirs hold no first-party source worth inventorying, and walking them
// on a large repository costs real time.
var skippedDirs = map[string]bool{
    ".git":         true,
    "node_modules": true,
    ".idea":        true,
    ".vscode":      true,
    "__pycache__":  true,
    ".venv":        true,
}

// generatedMarker is the convention every Go code generator follows.
var generatedMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// Walk inventories every recognised source file under root.
func Walk(root string) (*Inventory, error) {
    inv := &Inventory{Root: root, ByLanguage: map[string]int{}}

    err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
        // An unreadable entry is skipped rather than aborting the walk: a
        // permission error deep in a vendored tree should not fail a scan.
        if err != nil {
            return nil
        }
        if info.IsDir() {
            if skippedDirs[info.Name()] {
                return filepath.SkipDir
            }
            return nil
        }

        lang, ok := languageFor(info.Name())
        if !ok {
            return nil
        }

        rel, relErr := filepath.Rel(root, path)
        if relErr != nil {
            return nil
        }
        rel = filepath.ToSlash(rel)

        fi := FileInfo{
            Path:       rel,
            Language:   lang,
            Bytes:      info.Size(),
            IsTest:     IsTestPath(rel),
            IsVendored: IsVendoredPath(rel),
        }
        fi.Lines, fi.IsGenerated = scan(path)

        inv.Files = append(inv.Files, fi)
        inv.ByLanguage[lang]++
        inv.TotalBytes += fi.Bytes
        inv.TotalLines += fi.Lines
        return nil
    })

    return inv, err
}

func languageFor(name string) (string, bool) {
    if strings.EqualFold(name, "Dockerfile") || strings.HasPrefix(name, "Dockerfile.") {
        return "Dockerfile", true
    }
    lang, ok := languageByExt[strings.ToLower(filepath.Ext(name))]
    return lang, ok
}

// scan counts lines and looks for the generated-code marker, which by
// convention appears before the package clause.
func scan(path string) (lines int, generated bool) {
    f, err := os.Open(path)
    if err != nil {
        return 0, false
    }
    defer f.Close()

    sc := bufio.NewScanner(f)
    sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

    for sc.Scan() {
        lines++
        if !generated && lines <= 20 && generatedMarker.MatchString(strings.TrimSpace(sc.Text())) {
            generated = true
        }
    }
    return lines, generated
}

// IsTestPath and IsVendoredPath are exported because the ranking layer and the
// dataset miners must agree on what counts as test or vendored code. Two
// different answers would quietly corrupt both the report and the labels.
func IsTestPath(p string) bool {
    p = filepath.ToSlash(p)
    return strings.HasSuffix(p, "_test.go") ||
        strings.HasPrefix(p, "testdata/") ||
        strings.Contains(p, "/testdata/")
}

func IsVendoredPath(p string) bool {
    p = filepath.ToSlash(p)
    return strings.HasPrefix(p, "vendor/") || strings.Contains(p, "/vendor/")
}
