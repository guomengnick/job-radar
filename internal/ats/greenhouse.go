package ats

import (
	"context"
	"strconv"
	"time"
)

// Greenhouse: GET boards-api.greenhouse.io/v1/boards/{slug}/jobs?content=true
// returns every posting with its HTML content and offices in one response.
type Greenhouse struct{}

func (Greenhouse) Name() string { return "greenhouse" }
func (Greenhouse) Host() string { return "boards-api.greenhouse.io" }
func (Greenhouse) BoardURL(slug string) string {
	return "https://boards-api.greenhouse.io/v1/boards/" + slug + "/jobs?content=true"
}

type ghResponse struct {
	Jobs []struct {
		ID          int64  `json:"id"`
		Title       string `json:"title"`
		AbsoluteURL string `json:"absolute_url"`
		UpdatedAt   string `json:"updated_at"`
		FirstPub    string `json:"first_published"`
		Content     string `json:"content"`
		Location    struct {
			Name string `json:"name"`
		} `json:"location"`
		Offices []struct {
			Name     string `json:"name"`
			Location string `json:"location"`
		} `json:"offices"`
	} `json:"jobs"`
}

func (s Greenhouse) Fetch(ctx context.Context, g Getter, slug string) ([]Job, error) {
	var r ghResponse
	if err := g.GetJSON(ctx, s.BoardURL(slug), &r); err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(r.Jobs))
	for _, j := range r.Jobs {
		if j.AbsoluteURL == "" || j.Title == "" {
			continue
		}
		// "Remote" or "2 Locations" alone says little; the offices list
		// usually names the real places, so fold them in.
		parts := []string{j.Location.Name}
		for _, o := range j.Offices {
			parts = append(parts, o.Name, o.Location)
		}
		posted := parseTime(j.FirstPub)
		if posted.IsZero() {
			posted = parseTime(j.UpdatedAt)
		}
		jobs = append(jobs, Job{
			ATS: s.Name(), Board: slug, ID: strconv.FormatInt(j.ID, 10),
			Title: j.Title, URL: j.AbsoluteURL, Location: joinUnique(parts...),
			Description: HTMLToText(j.Content), PostedAt: posted,
		})
	}
	return jobs, nil
}

func parseTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}
