// Package metrics publishes run counters as CloudWatch Embedded Metric Format:
// one JSON log line becomes metrics, with no PutMetricData calls to pay for.
package metrics

import (
	"encoding/json"
	"io"
	"time"

	"github.com/guomengnick/job-radar/internal/store"
)

const Namespace = "JobRadar"

// The metrics carry no dimensions on purpose: every dimension value multiplies
// the billed metric count. Per-ATS detail lives in the runs table and the logs.
var names = []string{
	"RunSucceeded", "BoardsChecked", "BoardsNotFound", "BoardsThrottled",
	"BoardsFailed", "JobsNew", "DurationSeconds",
}

// EmitScan writes one EMF line for a finished scan run.
func EmitScan(w io.Writer, r store.Run) error {
	failed := 0
	for k, v := range r.Outcomes {
		if k != "ok" && k != "not_found" && k != "throttled" {
			failed += v
		}
	}
	succeeded := 0
	if r.Status == "ok" || r.Status == "partial" {
		succeeded = 1
	}
	values := map[string]float64{
		"RunSucceeded":    float64(succeeded),
		"BoardsChecked":   float64(r.BoardsChecked),
		"BoardsNotFound":  float64(r.Outcomes["not_found"]),
		"BoardsThrottled": float64(r.Outcomes["throttled"]),
		"BoardsFailed":    float64(failed),
		"JobsNew":         float64(r.JobsNew),
		"DurationSeconds": r.Duration().Seconds(),
	}
	defs := make([]map[string]string, 0, len(names))
	for _, n := range names {
		unit := "Count"
		if n == "DurationSeconds" {
			unit = "Seconds"
		}
		defs = append(defs, map[string]string{"Name": n, "Unit": unit})
	}
	line := map[string]any{
		"_aws": map[string]any{
			"Timestamp": r.FinishedAt.UnixMilli(),
			"CloudWatchMetrics": []map[string]any{{
				"Namespace": Namespace, "Dimensions": [][]string{{}}, "Metrics": defs,
			}},
		},
		"ats":    r.ATS,
		"status": r.Status,
	}
	for k, v := range values {
		line[k] = v
	}
	if r.FinishedAt.IsZero() {
		line["_aws"].(map[string]any)["Timestamp"] = time.Now().UnixMilli()
	}
	return json.NewEncoder(w).Encode(line)
}
