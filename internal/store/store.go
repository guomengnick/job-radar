// Package store keeps the radar's state: which boards exist and how they
// behaved, which postings were already seen, and what each run did.
// The same interface is backed by a local file (development) and DynamoDB (cloud).
package store

import (
	"context"
	"time"
)

type BoardStatus string

const (
	BoardUnknown BoardStatus = "unknown" // never checked
	BoardOK      BoardStatus = "ok"
	BoardFailing BoardStatus = "failing" // last check failed for a reason other than 404
	BoardDead    BoardStatus = "dead"    // N consecutive 404s; rechecked weekly
)

type Board struct {
	ATS            string      `json:"ats" dynamodbav:"ats"`
	Slug           string      `json:"slug" dynamodbav:"slug"`
	Status         BoardStatus `json:"status" dynamodbav:"status"`
	Consecutive404 int         `json:"consecutive_404" dynamodbav:"consecutive_404"`
	LastOutcome    string      `json:"last_outcome,omitempty" dynamodbav:"last_outcome,omitempty"`
	LastCheckedAt  time.Time   `json:"last_checked_at" dynamodbav:"last_checked_at"`
	LastOKAt       time.Time   `json:"last_ok_at" dynamodbav:"last_ok_at"`
	JobCount       int         `json:"job_count" dynamodbav:"job_count"`
}

type JobState string

const (
	JobNew      JobState = "new"      // passed the notify threshold, not yet sent
	JobSeen     JobState = "seen"     // matched the title filter but below the threshold
	JobNotified JobState = "notified" // sent in a digest
	JobSilent   JobState = "silent"   // recorded during a silent run (e.g. after a rule change)
)

type JobRecord struct {
	Key        string    `json:"key" dynamodbav:"key"`
	ATS        string    `json:"ats" dynamodbav:"ats"`
	Board      string    `json:"board" dynamodbav:"board"`
	Title      string    `json:"title" dynamodbav:"title"`
	URL        string    `json:"url" dynamodbav:"url"`
	Location   string    `json:"location" dynamodbav:"location"`
	WorkMode   string    `json:"work_mode,omitempty" dynamodbav:"work_mode,omitempty"`
	Class      string    `json:"class" dynamodbav:"class"`
	Reason     string    `json:"reason" dynamodbav:"reason"`
	Score      int       `json:"score" dynamodbav:"score"`
	Skills     []string  `json:"skills,omitempty" dynamodbav:"skills,omitempty"`
	Salary     string    `json:"salary,omitempty" dynamodbav:"salary,omitempty"` // as posted; 104 only
	Notes      []string  `json:"notes,omitempty" dynamodbav:"notes,omitempty"`   // e.g. 兼職; 104 only
	PostedAt   time.Time `json:"posted_at" dynamodbav:"posted_at"`
	FirstSeen  time.Time `json:"first_seen" dynamodbav:"first_seen"`
	State      JobState  `json:"state" dynamodbav:"state"`
	NotifiedAt time.Time `json:"notified_at" dynamodbav:"notified_at"`
	// Pending is set only while State == new, which makes the DynamoDB
	// pending-index a sparse index holding just the unsent jobs.
	Pending string `json:"-" dynamodbav:"pending,omitempty"`
}

type Run struct {
	ATS           string         `json:"ats" dynamodbav:"ats"`
	Task          string         `json:"task" dynamodbav:"task"`
	StartedAt     time.Time      `json:"started_at" dynamodbav:"started_at"`
	FinishedAt    time.Time      `json:"finished_at" dynamodbav:"finished_at"`
	Status        string         `json:"status" dynamodbav:"status"` // ok | partial | failed
	Silent        bool           `json:"silent,omitempty" dynamodbav:"silent,omitempty"`
	BoardsTotal   int            `json:"boards_total" dynamodbav:"boards_total"`
	BoardsDue     int            `json:"boards_due" dynamodbav:"boards_due"`
	BoardsChecked int            `json:"boards_checked" dynamodbav:"boards_checked"`
	Outcomes      map[string]int `json:"outcomes" dynamodbav:"outcomes"`
	JobsFetched   int            `json:"jobs_fetched" dynamodbav:"jobs_fetched"`
	JobsMatched   int            `json:"jobs_matched" dynamodbav:"jobs_matched"` // passed the title filter
	JobsNew       int            `json:"jobs_new" dynamodbav:"jobs_new"`         // first time seen
	JobsPending   int            `json:"jobs_pending" dynamodbav:"jobs_pending"` // new and above threshold
	NewByClass    map[string]int `json:"new_by_class" dynamodbav:"new_by_class"`
	NewlyDead     int            `json:"newly_dead" dynamodbav:"newly_dead"`
	Revived       int            `json:"revived" dynamodbav:"revived"`
	Error         string         `json:"error,omitempty" dynamodbav:"error,omitempty"`
}

func (r Run) Duration() time.Duration { return r.FinishedAt.Sub(r.StartedAt) }

// Assessment is one 送到雷達 judgement of a page the user sent, kept to
// compare the model's verdicts with what the user went on to apply to.
type Assessment struct {
	Key        string    `json:"key" dynamodbav:"key"` // "assess#" + the job's key, or its URL
	URL        string    `json:"url" dynamodbav:"url"`
	Title      string    `json:"title" dynamodbav:"title"`
	Company    string    `json:"company" dynamodbav:"company"`
	Score      int       `json:"score" dynamodbav:"score"`
	Verdict    string    `json:"verdict" dynamodbav:"verdict"`
	Model      string    `json:"model" dynamodbav:"model"`
	AssessedAt time.Time `json:"assessed_at" dynamodbav:"assessed_at"`
	Detail     string    `json:"detail" dynamodbav:"detail"` // the full judgement as JSON
	// JD is the page it judged, kept in the pages bucket (for example
	// jd/104/abc.json); empty when keeping it failed or before 2026-10-25.
	JD string `json:"jd,omitempty" dynamodbav:"jd,omitempty"`
}

// Batch is one set of pages sent with the extension's batch button, kept
// from submission until its summary goes out.
type Batch struct {
	Key string `json:"key" dynamodbav:"key"` // "batch#" + the Anthropic batch ID
	// Pending is "batch" until the summary is sent, which puts the record in
	// pending-index (the digest reads only "new" there).
	Pending     string      `json:"-" dynamodbav:"pending,omitempty"`
	SubmittedAt time.Time   `json:"submitted_at" dynamodbav:"first_seen"` // pending-index's sort key
	BatchID     string      `json:"batch_id" dynamodbav:"batch_id"`
	Model       string      `json:"model" dynamodbav:"model"`
	Items       []BatchItem `json:"items" dynamodbav:"items"`
	// Left out before the model: excluded by a rule, judged recently, or
	// not readable in the browser.
	Excluded []BatchItem `json:"excluded,omitempty" dynamodbav:"excluded,omitempty"`
	Skipped  []BatchItem `json:"skipped,omitempty" dynamodbav:"skipped,omitempty"`
	ClosedAt time.Time   `json:"closed_at" dynamodbav:"closed_at"`
}

// BatchItem is one page of a batch: ID names its request in the batch, Key
// is the job's key, Reason why it was left out, if it was.
type BatchItem struct {
	ID     string `json:"id,omitempty" dynamodbav:"id,omitempty"`
	Key    string `json:"key" dynamodbav:"key"`
	URL    string `json:"url" dynamodbav:"url"`
	Title  string `json:"title" dynamodbav:"title"`
	Reason string `json:"reason,omitempty" dynamodbav:"reason,omitempty"`
	JD     string `json:"jd,omitempty" dynamodbav:"jd,omitempty"` // where the page is kept, when it went to the model
}

// Label is the user's own verdict on a judged job, from a Telegram button,
// for evaluating the profile (radar eval). The latest press counts.
type Label struct {
	Key       string    `json:"key" dynamodbav:"key"`   // "label#" + the job's key
	Want      string    `json:"want" dynamodbav:"want"` // 建議投 | 可考慮 | 不建議
	LabeledAt time.Time `json:"labeled_at" dynamodbav:"labeled_at"`
}

// BatchPending marks an open batch in pending-index.
const BatchPending = "batch"

type Store interface {
	ListBoards(ctx context.Context, ats string) ([]Board, error)
	SaveBoards(ctx context.Context, boards []Board) error
	// AddBoards inserts slugs not yet known for ats and returns how many were new.
	AddBoards(ctx context.Context, ats string, slugs []string) (int, error)
	// InsertJobIfNew stores j unless its key exists; true means it was new.
	InsertJobIfNew(ctx context.Context, j JobRecord) (bool, error)
	PendingJobs(ctx context.Context) ([]JobRecord, error)
	// JobsSince returns the jobs from one source (the ats field) first seen
	// at or after since, oldest first, whatever their state.
	JobsSince(ctx context.Context, ats string, since time.Time) ([]JobRecord, error)
	MarkNotified(ctx context.Context, keys []string, at time.Time) error
	SaveRun(ctx context.Context, r Run) error
	RunsSince(ctx context.Context, since time.Time) ([]Run, error)
	// SaveAssessment stores a judgement, replacing an earlier one of the
	// same page.
	SaveAssessment(ctx context.Context, a Assessment) error
	// AssessmentOf returns the judgement stored under key ("assess#…").
	AssessmentOf(ctx context.Context, key string) (Assessment, bool, error)
	// SaveBatch stores a batch; with Pending set it is open.
	SaveBatch(ctx context.Context, b Batch) error
	// OpenBatches returns the batches whose summary has not gone out.
	OpenBatches(ctx context.Context) ([]Batch, error)
	// CloseBatch takes a batch out of the open ones.
	CloseBatch(ctx context.Context, key string, at time.Time) error
	// SaveLabel stores the user's verdict on a job, replacing an earlier one.
	SaveLabel(ctx context.Context, l Label) error
	// Labels returns every label. In DynamoDB it reads the whole table, so
	// only the radar command calls it, now and then.
	Labels(ctx context.Context) ([]Label, error)
}
