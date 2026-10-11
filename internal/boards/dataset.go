// Package boards loads the list of company boards to scan from the public
// job-board-aggregator dataset (one JSON array of slugs per ATS).
package boards

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/guomengnick/job-radar/internal/ats"
)

// DatasetURL is the slug list for one ATS.
func DatasetURL(base, atsName string) string {
	return strings.TrimRight(base, "/") + "/" + atsName + "_companies.json"
}

// FetchSlugs downloads and validates the slug list for atsName.
func FetchSlugs(ctx context.Context, g ats.Getter, base, atsName string) ([]string, error) {
	var raw []any
	if err := g.GetJSON(ctx, DatasetURL(base, atsName), &raw); err != nil {
		return nil, fmt.Errorf("dataset %s: %w", atsName, err)
	}
	return Clean(raw), nil
}

// ReadSlugsFile reads a local JSON array, e.g. career-ops data/cache/ats-companies/<ats>.json.
func ReadSlugsFile(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw []any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return Clean(raw), nil
}

// Clean keeps string entries that are safe to put into a URL, deduplicated.
func Clean(raw []any) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if !ats.SlugRE.MatchString(s) || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
