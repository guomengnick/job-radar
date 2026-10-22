// Package config holds the radar's rules. The default file is embedded at
// build time so the deployed binary carries the exact rules it was reviewed with.
package config

import (
	_ "embed"
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

//go:embed radar.yml
var defaultYAML []byte

type Config struct {
	Title    TitleRules    `yaml:"title"`
	Location LocationRules `yaml:"location"`
	Skills   []Skill       `yaml:"skills"`
	Notify   NotifyRules   `yaml:"notify"`
	Scan     ScanRules     `yaml:"scan"`
	Boards   BoardRules    `yaml:"boards"`
	// Springboard judges Taichung office jobs from 104 alerts.
	Springboard SpringboardRules `yaml:"springboard"`
	Assess      AssessRules      `yaml:"assess"`
}

// AssessRules configure the model that judges a page sent from the browser.
type AssessRules struct {
	Model string `yaml:"model"`
	// Effort is how hard the model thinks: low, medium, high, xhigh or max;
	// empty leaves the model's default.
	Effort string `yaml:"effort"`
}

type SpringboardRules struct {
	TitlePlus        []string `yaml:"title_plus"`
	TitleExclude     []string `yaml:"title_exclude"`
	MinAnnualSalary  int      `yaml:"min_annual_salary"`
	MinMonthlyTopPay int      `yaml:"min_monthly_top_pay"`
	// Notes tag a job in the digest; they never drop one.
	Notes []NoteRule `yaml:"notes"`
}

// NoteRule tags a job whose title, description or tags contain any of Words.
type NoteRule struct {
	Name  string   `yaml:"name"`
	Words []string `yaml:"words"`
}

type TitleRules struct {
	Positive []string `yaml:"positive"`
	Negative []string `yaml:"negative"`
}

type LocationRules struct {
	TaipeiHybridMaxDays  float64 `yaml:"taipei_hybrid_max_days"`
	HsinchuHybridMaxDays float64 `yaml:"hsinchu_hybrid_max_days"`
}

type Skill struct {
	Name    string `yaml:"name"`
	Pattern string `yaml:"pattern"`
	Weight  int    `yaml:"weight"`
}

type NotifyRules struct {
	ClassAMinScore int `yaml:"class_a_min_score"`
	ClassBMinScore int `yaml:"class_b_min_score"`
	MaxItems       int `yaml:"max_items"`
}

type ScanRules struct {
	RPS                   map[string]float64 `yaml:"rps"`
	Concurrency           int                `yaml:"concurrency"`
	DeadAfter404          int                `yaml:"dead_after_404"`
	DeadRecheckDays       int                `yaml:"dead_recheck_days"`
	DeadlineBufferSeconds int                `yaml:"deadline_buffer_seconds"`
}

type BoardRules struct {
	DatasetBase string              `yaml:"dataset_base"`
	Extra       map[string][]string `yaml:"extra"`
}

// Default returns the embedded configuration.
func Default() (*Config, error) { return Parse(defaultYAML) }

// Load reads a config file, or the embedded default when path is empty.
func Load(path string) (*Config, error) {
	if path == "" {
		return Default()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	if len(c.Title.Positive) == 0 {
		return fmt.Errorf("config: title.positive is empty")
	}
	for _, s := range c.Skills {
		if _, err := regexp.Compile(s.Pattern); err != nil {
			return fmt.Errorf("config: skill %q: %w", s.Name, err)
		}
	}
	if c.Scan.Concurrency <= 0 {
		c.Scan.Concurrency = 8
	}
	if c.Scan.DeadAfter404 <= 0 {
		c.Scan.DeadAfter404 = 3
	}
	if c.Scan.DeadRecheckDays <= 0 {
		c.Scan.DeadRecheckDays = 7
	}
	if c.Springboard.MinAnnualSalary <= 0 || c.Springboard.MinMonthlyTopPay <= 0 {
		return fmt.Errorf("config: springboard salary floors must be set")
	}
	if c.Notify.MaxItems <= 0 {
		c.Notify.MaxItems = 25
	}
	if c.Assess.Model == "" {
		return fmt.Errorf("config: assess.model must be set")
	}
	switch c.Assess.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("config: assess.effort %q is not low, medium, high, xhigh or max", c.Assess.Effort)
	}
	return nil
}
