// Package github holds the pull request integration.
//
// Order of work, deliberately: a GitHub Action that uploads SARIF comes first,
// because it needs no server, no webhook and no app key. The App follows only
// once findings are good enough that a maintainer would want them inline.
//
// Nothing here is implemented yet.
package github

// Config is the per-repository configuration, read from .truebug.yml.
type Config struct {
    // MaxComments caps comments per pull request. Two is the design target.
    // A bot that leaves twelve comments gets muted, and then the good finding
    // goes unread along with the rest.
    MaxComments int `yaml:"max_comments"`

    // MinPriority is the floor for posting: low|medium|high|critical.
    MinPriority string `yaml:"min_priority"`

    // RequireEvidenceLevel is the floor on the evidence ladder. Set above
    // static_fact, and only findings confirmed by execution are posted.
    RequireEvidenceLevel string `yaml:"require_evidence_level"`

    // Exclude are path globs never reported on.
    Exclude []string `yaml:"exclude"`

    // DisabledRules are rules this project has opted out of.
    DisabledRules []string `yaml:"disabled_rules"`
}

// DefaultConfig is deliberately conservative. A new installation should be
// quiet by default; a maintainer can always ask for more.
func DefaultConfig() Config {
    return Config{
        MaxComments:          2,
        MinPriority:          "high",
        RequireEvidenceLevel: "static_fact",
        Exclude:              []string{"vendor/**", "**/*_generated.go", "**/zz_generated.*.go"},
    }
}

// Permissions is the least-privilege set the App requests.
//
// No write access to code, no access to secrets, no organisation scope. The
// App reads pull requests and writes checks and comments. Anything more is a
// liability on a tool that runs untrusted code.
var Permissions = map[string]string{
    "contents":      "read",
    "pull_requests": "write",
    "checks":        "write",
    "metadata":      "read",
}
