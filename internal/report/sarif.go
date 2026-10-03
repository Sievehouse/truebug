// Package report renders a scan in the formats other tools and humans read.
package report

import (
    "encoding/json"
    "io"
    "sort"

    "github.com/Sievehouse/truebug/internal/findings"
)

const sarifSchema = "https://json.schemastore.org/sarif-2.1.0.json"

type sarifLog struct {
    Schema  string     `json:"$schema"`
    Version string     `json:"version"`
    Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
    Tool    sarifTool     `json:"tool"`
    Results []sarifResult `json:"results"`
}

type sarifTool struct {
    Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
    Name           string      `json:"name"`
    InformationURI string      `json:"informationUri"`
    Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
    ID               string         `json:"id"`
    ShortDescription sarifText      `json:"shortDescription"`
    Properties       sarifRuleProps `json:"properties"`
}

type sarifRuleProps struct {
    Category string   `json:"category,omitempty"`
    Tags     []string `json:"tags,omitempty"`
}

type sarifText struct {
    Text string `json:"text"`
}

type sarifResult struct {
    RuleID              string                 `json:"ruleId"`
    Level               string                 `json:"level"`
    Message             sarifText              `json:"message"`
    Locations           []sarifLocation        `json:"locations"`
    PartialFingerprints map[string]string      `json:"partialFingerprints,omitempty"`
    Properties          map[string]interface{} `json:"properties,omitempty"`
}

type sarifLocation struct {
    PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
    ArtifactLocation sarifArtifact `json:"artifactLocation"`
    Region           sarifRegion   `json:"region"`
}

type sarifArtifact struct {
    URI string `json:"uri"`
}

type sarifRegion struct {
    StartLine   int `json:"startLine"`
    StartColumn int `json:"startColumn,omitempty"`
}

// SARIF writes the report as SARIF 2.1.0, which GitHub code scanning ingests
// directly. This is the cheapest useful integration: no App, no webhook, no
// permissions beyond the workflow's own.
//
// Our fingerprint goes into partialFingerprints, which is how GitHub tracks a
// finding across commits without treating every reformatting as a new alert.
func SARIF(w io.Writer, r findings.Report) error {
    rules := map[string]sarifRule{}
    results := make([]sarifResult, 0, len(r.Findings))

    for _, f := range r.Findings {
        if _, ok := rules[f.Rule]; !ok {
            rules[f.Rule] = sarifRule{
                ID:               f.Rule,
                ShortDescription: sarifText{Text: f.Rule},
                Properties:       sarifRuleProps{Category: f.Category, Tags: []string{f.Tool}},
            }
        }

        // SARIF regions are 1-based and a zero line is invalid.
        line := f.Line
        if line < 1 {
            line = 1
        }

        results = append(results, sarifResult{
            RuleID:  f.Rule,
            Level:   sarifLevel(f.Priority),
            Message: sarifText{Text: f.Message},
            Locations: []sarifLocation{{
                PhysicalLocation: sarifPhysical{
                    ArtifactLocation: sarifArtifact{URI: f.File},
                    Region:           sarifRegion{StartLine: line, StartColumn: f.Column},
                },
            }},
            PartialFingerprints: map[string]string{"truebug/v1": f.Fingerprint},
            Properties: map[string]interface{}{
                "score":        f.Score,
                "priority":     f.Priority,
                "tool":         f.Tool,
                "toolSeverity": f.Severity,
                "isTest":       f.IsTest,
            },
        })
    }

    ids := make([]string, 0, len(rules))
    for id := range rules {
        ids = append(ids, id)
    }
    sort.Strings(ids)

    ruleList := make([]sarifRule, 0, len(ids))
    for _, id := range ids {
        ruleList = append(ruleList, rules[id])
    }

    log := sarifLog{
        Schema:  sarifSchema,
        Version: "2.1.0",
        Runs: []sarifRun{{
            Tool: sarifTool{Driver: sarifDriver{
                Name:           "truebug",
                InformationURI: "https://github.com/Sievehouse/truebug",
                Rules:          ruleList,
            }},
            Results: results,
        }},
    }

    enc := json.NewEncoder(w)
    enc.SetIndent("", "  ")
    return enc.Encode(log)
}

// sarifLevel maps our priority onto SARIF levels. Nothing maps to "error"
// unless we rate it high or critical ourselves: the tool's own severity is not
// comparable across tools and is deliberately not used here.
func sarifLevel(priority string) string {
    switch priority {
    case "critical", "high":
        return "error"
    case "medium":
        return "warning"
    default:
        return "note"
    }
}
