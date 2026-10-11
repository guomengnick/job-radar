package ats

import (
	"context"
	"strings"
	"time"
)

// Lever: GET api.lever.co/v0/postings/{slug}?mode=json returns a JSON array
// of every posting on the board.
type Lever struct{}

func (Lever) Name() string { return "lever" }
func (Lever) Host() string { return "api.lever.co" }
func (Lever) BoardURL(slug string) string {
	return "https://api.lever.co/v0/postings/" + slug + "?mode=json"
}

type leverPosting struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	HostedURL  string `json:"hostedUrl"`
	CreatedAt  int64  `json:"createdAt"`
	Workplace  string `json:"workplaceType"`
	Country    string `json:"country"`
	Desc       string `json:"descriptionPlain"`
	Additional string `json:"additionalPlain"`
	Categories struct {
		Location     string   `json:"location"`
		AllLocations []string `json:"allLocations"`
	} `json:"categories"`
	Lists []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	} `json:"lists"`
}

func (s Lever) Fetch(ctx context.Context, g Getter, slug string) ([]Job, error) {
	var postings []leverPosting
	if err := g.GetJSON(ctx, s.BoardURL(slug), &postings); err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(postings))
	for _, p := range postings {
		if p.HostedURL == "" || p.Text == "" {
			continue
		}
		// categories.location holds one primary city; allLocations has the
		// rest of a multi-location posting.
		loc := joinUnique(append([]string{p.Categories.Location}, p.Categories.AllLocations...)...)
		var desc strings.Builder
		desc.WriteString(p.Desc)
		for _, l := range p.Lists {
			desc.WriteString("\n" + l.Text + "\n" + HTMLToText(l.Content))
		}
		desc.WriteString("\n" + p.Additional)
		var posted time.Time
		if p.CreatedAt > 0 {
			posted = time.UnixMilli(p.CreatedAt).UTC()
		}
		jobs = append(jobs, Job{
			ATS: s.Name(), Board: slug, ID: p.ID, Title: p.Text, URL: p.HostedURL,
			Location: loc, WorkplaceType: normalizeWorkplace(p.Workplace),
			Description: strings.TrimSpace(desc.String()), PostedAt: posted,
		})
	}
	return jobs, nil
}
