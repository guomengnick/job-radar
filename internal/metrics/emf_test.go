package metrics

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// emf decodes one line and returns the values of the declared metrics, so a
// metric that is declared but never set (or set but not declared) shows up.
func emf(t *testing.T, line []byte) map[string]float64 {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(line, &doc); err != nil {
		t.Fatal(err)
	}
	aws := doc["_aws"].(map[string]any)
	cw := aws["CloudWatchMetrics"].([]any)[0].(map[string]any)
	if cw["Namespace"] != Namespace {
		t.Fatalf("namespace = %v", cw["Namespace"])
	}
	out := map[string]float64{}
	for _, m := range cw["Metrics"].([]any) {
		name := m.(map[string]any)["Name"].(string)
		v, ok := doc[name].(float64)
		if !ok {
			t.Fatalf("metric %s declared but has no value", name)
		}
		out[name] = v
	}
	return out
}

func TestEmitMail(t *testing.T) {
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		status        string
		alert         bool
		failed, alrts float64
	}{
		{"failed", false, 1, 0}, {"ignored", false, 0, 0}, {"unparsed", false, 0, 0}, {"parsed", true, 0, 1}, {"parsed", false, 0, 0},
	} {
		var b bytes.Buffer
		if err := EmitMail(&b, c.status, c.alert, at); err != nil {
			t.Fatal(err)
		}
		got := emf(t, b.Bytes())
		if got["MailReceived"] != 1 || got["MailFailed"] != c.failed || got["AlertsParsed"] != c.alrts || len(got) != 3 {
			t.Errorf("%s %v: %v", c.status, c.alert, got)
		}
	}
}
