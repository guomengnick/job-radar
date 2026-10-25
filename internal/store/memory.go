package store

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Memory is an in-process Store. With a path it persists to a JSON file, so
// local runs remember what they have seen; without one it is a test double.
type Memory struct {
	mu   sync.Mutex
	path string
	data memoryData
}

type memoryData struct {
	Boards map[string]map[string]Board `json:"boards"`
	Jobs   map[string]JobRecord        `json:"jobs"`
	Runs   []Run                       `json:"runs"`
	// Assessments by key; nil in state files written before they existed.
	Assessments map[string]Assessment `json:"assessments,omitempty"`
	Batches     map[string]Batch      `json:"batches,omitempty"`
	Labels      map[string]Label      `json:"labels,omitempty"`
}

func NewMemory() *Memory {
	return &Memory{data: memoryData{Boards: map[string]map[string]Board{}, Jobs: map[string]JobRecord{}}}
}

// OpenFile loads path if it exists; Flush writes it back atomically.
func OpenFile(path string) (*Memory, error) {
	m := NewMemory()
	m.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m.data); err != nil {
		return nil, err
	}
	if m.data.Boards == nil {
		m.data.Boards = map[string]map[string]Board{}
	}
	if m.data.Jobs == nil {
		m.data.Jobs = map[string]JobRecord{}
	}
	return m, nil
}

func (m *Memory) Flush() error {
	if m.path == "" {
		return nil
	}
	m.mu.Lock()
	b, err := json.MarshalIndent(m.data, "", " ")
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func (m *Memory) ListBoards(_ context.Context, ats string) ([]Board, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Board, 0, len(m.data.Boards[ats]))
	for _, b := range m.data.Boards[ats] {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (m *Memory) SaveBoards(_ context.Context, boards []Board) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range boards {
		if m.data.Boards[b.ATS] == nil {
			m.data.Boards[b.ATS] = map[string]Board{}
		}
		m.data.Boards[b.ATS][b.Slug] = b
	}
	return nil
}

func (m *Memory) AddBoards(_ context.Context, ats string, slugs []string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data.Boards[ats] == nil {
		m.data.Boards[ats] = map[string]Board{}
	}
	added := 0
	for _, s := range slugs {
		if _, ok := m.data.Boards[ats][s]; !ok {
			m.data.Boards[ats][s] = Board{ATS: ats, Slug: s, Status: BoardUnknown}
			added++
		}
	}
	return added, nil
}

func (m *Memory) InsertJobIfNew(_ context.Context, j JobRecord) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data.Jobs[j.Key]; ok {
		return false, nil
	}
	m.data.Jobs[j.Key] = j
	return true, nil
}

func (m *Memory) PendingJobs(_ context.Context) ([]JobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []JobRecord
	for _, j := range m.data.Jobs {
		if j.State == JobNew {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].FirstSeen.Before(out[k].FirstSeen) })
	return out, nil
}

func (m *Memory) JobsSince(_ context.Context, ats string, since time.Time) ([]JobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []JobRecord
	for _, j := range m.data.Jobs {
		if j.ATS == ats && !j.FirstSeen.Before(since) {
			out = append(out, j)
		}
	}
	// Jobs from one alert share a time; the key keeps their order fixed.
	sort.Slice(out, func(i, k int) bool {
		if !out[i].FirstSeen.Equal(out[k].FirstSeen) {
			return out[i].FirstSeen.Before(out[k].FirstSeen)
		}
		return out[i].Key < out[k].Key
	})
	return out, nil
}

func (m *Memory) MarkNotified(_ context.Context, keys []string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		if j, ok := m.data.Jobs[k]; ok {
			j.State, j.NotifiedAt, j.Pending = JobNotified, at, ""
			m.data.Jobs[k] = j
		}
	}
	return nil
}

func (m *Memory) SaveRun(_ context.Context, r Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Runs = append(m.data.Runs, r)
	return nil
}

func (m *Memory) SaveAssessment(_ context.Context, a Assessment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data.Assessments == nil {
		m.data.Assessments = map[string]Assessment{}
	}
	m.data.Assessments[a.Key] = a
	return nil
}

func (m *Memory) AssessmentOf(_ context.Context, key string) (Assessment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.data.Assessments[key]
	return a, ok, nil
}

func (m *Memory) SaveBatch(_ context.Context, b Batch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data.Batches == nil {
		m.data.Batches = map[string]Batch{}
	}
	m.data.Batches[b.Key] = b
	return nil
}

func (m *Memory) OpenBatches(_ context.Context) ([]Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Batch
	for _, b := range m.data.Batches {
		if b.Pending == BatchPending {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].SubmittedAt.Before(out[k].SubmittedAt) })
	return out, nil
}

func (m *Memory) CloseBatch(_ context.Context, key string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.data.Batches[key]; ok {
		b.Pending, b.ClosedAt = "", at
		m.data.Batches[key] = b
	}
	return nil
}

// Assessment returns the judgement stored under key, for tests and the CLI.
func (m *Memory) Assessment(key string) (Assessment, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.data.Assessments[key]
	return a, ok
}

func (m *Memory) RunsSince(_ context.Context, since time.Time) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Run
	for _, r := range m.data.Runs {
		if !r.StartedAt.Before(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *Memory) SaveLabel(_ context.Context, l Label) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data.Labels == nil {
		m.data.Labels = map[string]Label{}
	}
	m.data.Labels[l.Key] = l
	return nil
}

func (m *Memory) Labels(_ context.Context) ([]Label, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Label, 0, len(m.data.Labels))
	for _, l := range m.data.Labels {
		out = append(out, l)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Key < out[k].Key })
	return out, nil
}
