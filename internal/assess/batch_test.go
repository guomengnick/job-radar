package assess

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBatch(t *testing.T) {
	var submitted map[string]any
	status := "in_progress"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/batches":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &submitted)
			w.Header().Set("content-type", "application/json")
			io.WriteString(w, `{"id":"msgbatch_1","type":"message_batch","processing_status":"in_progress"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/messages/batches/msgbatch_1":
			w.Header().Set("content-type", "application/json")
			io.WriteString(w, `{"id":"msgbatch_1","type":"message_batch","processing_status":"`+status+`"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/messages/batches/msgbatch_1/results":
			w.Header().Set("content-type", "application/x-jsonl")
			msg := strings.ReplaceAll(goodReply, "\n", "")
			io.WriteString(w, `{"custom_id":"p0","result":{"type":"succeeded","message":`+msg+`}}`+"\n"+
				`{"custom_id":"p1","result":{"type":"errored","error":{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}}}`+"\n"+
				`{"custom_id":"p2","result":{"type":"expired"}}`+"\n")
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := Client{APIKey: "k", Model: "claude-test", Effort: "low", BaseURL: srv.URL}
	ctx := context.Background()

	id, err := c.Submit(ctx, "技能：C#", []BatchItem{
		{ID: "p0", Page: Page{URL: "https://www.104.com.tw/job/aaa01", Text: "後端"}},
		{ID: "p1", Page: Page{URL: "https://www.104.com.tw/job/bbb01", Text: "資料"}},
	})
	if err != nil || id != "msgbatch_1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	reqs := submitted["requests"].([]any)
	first := reqs[0].(map[string]any)
	params := first["params"].(map[string]any)
	system := params["system"].([]any)
	cache := system[len(system)-1].(map[string]any)["cache_control"].(map[string]any)
	if len(reqs) != 2 || first["custom_id"] != "p0" || params["model"] != "claude-test" ||
		params["output_config"].(map[string]any)["effort"] != "low" || cache["ttl"] != "1h" ||
		!strings.Contains(userText(t, params), "後端") {
		t.Fatalf("request = %v", first)
	}

	if ended, err := c.Ended(ctx, id); err != nil || ended {
		t.Fatalf("in progress: ended=%v err=%v", ended, err)
	}
	status = "ended"
	if ended, err := c.Ended(ctx, id); err != nil || !ended {
		t.Fatalf("ended=%v err=%v", ended, err)
	}

	res, err := c.Results(ctx, id)
	if err != nil || len(res) != 3 {
		t.Fatalf("results=%+v err=%v", res, err)
	}
	if r := res[0]; r.ID != "p0" || r.Err != nil || r.Assessment.Score != 82 || r.Usage.InputTokens != 3100 {
		t.Errorf("succeeded: %+v", r)
	}
	if r := res[1]; r.ID != "p1" || r.Err == nil || !strings.Contains(r.Err.Error(), "prompt is too long") {
		t.Errorf("errored: %+v", r)
	}
	if r := res[2]; r.Err == nil || !strings.Contains(r.Err.Error(), "24 hours") {
		t.Errorf("expired: %+v", r)
	}
}
