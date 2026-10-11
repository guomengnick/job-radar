package ats

import "context"

// Ashby: GET api.ashbyhq.com/posting-api/job-board/{slug} returns
// {"jobs":[...]} with plain-text descriptions and structured locations.
type Ashby struct{}

func (Ashby) Name() string { return "ashby" }
func (Ashby) Host() string { return "api.ashbyhq.com" }
func (Ashby) BoardURL(slug string) string {
	return "https://api.ashbyhq.com/posting-api/job-board/" + slug
}

type ashbyResponse struct {
	Jobs []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		JobURL      string `json:"jobUrl"`
		Location    string `json:"location"`
		Workplace   string `json:"workplaceType"`
		IsRemote    *bool  `json:"isRemote"`
		IsListed    *bool  `json:"isListed"`
		PublishedAt string `json:"publishedAt"`
		Desc        string `json:"descriptionPlain"`
		Secondary   []struct {
			Location string `json:"location"`
			Address  struct {
				PostalAddress struct {
					Locality string `json:"addressLocality"`
					Country  string `json:"addressCountry"`
				} `json:"postalAddress"`
			} `json:"address"`
		} `json:"secondaryLocations"`
	} `json:"jobs"`
}

func (s Ashby) Fetch(ctx context.Context, g Getter, slug string) ([]Job, error) {
	var r ashbyResponse
	if err := g.GetJSON(ctx, s.BoardURL(slug), &r); err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(r.Jobs))
	for _, j := range r.Jobs {
		if j.JobURL == "" || j.Title == "" || (j.IsListed != nil && !*j.IsListed) {
			continue
		}
		parts := []string{j.Location}
		for _, sl := range j.Secondary {
			parts = append(parts, sl.Location, sl.Address.PostalAddress.Locality, sl.Address.PostalAddress.Country)
		}
		wp := normalizeWorkplace(j.Workplace)
		if wp == "" && j.IsRemote != nil && *j.IsRemote {
			wp = "remote"
		}
		jobs = append(jobs, Job{
			ATS: s.Name(), Board: slug, ID: j.ID, Title: j.Title, URL: j.JobURL,
			Location: joinUnique(parts...), WorkplaceType: wp,
			Description: j.Desc, PostedAt: parseTime(j.PublishedAt),
		})
	}
	return jobs, nil
}
