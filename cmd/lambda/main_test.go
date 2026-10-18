package main

import (
	"encoding/json"
	"testing"
)

func TestS3Records(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantKey string // "" means: not an S3 notification
	}{
		{
			name: "S3 notification, key is URL-decoded",
			payload: `{"Records":[{"eventSource":"aws:s3","eventName":"ObjectCreated:Put",
				"s3":{"bucket":{"name":"job-radar-mail-1"},"object":{"key":"inbound/a%2Bb+c"}}}]}`,
			wantKey: "inbound/a+b c",
		},
		{name: "scheduler scan task", payload: `{"task":"scan","ats":"greenhouse"}`},
		{name: "digest task", payload: `{"task":"digest"}`},
		{name: "records from another source", payload: `{"Records":[{"eventSource":"aws:sqs"}]}`},
		{name: "empty records", payload: `{"Records":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs, ok := s3Records(json.RawMessage(tt.payload))
			if ok != (tt.wantKey != "") {
				t.Fatalf("ok = %v", ok)
			}
			if ok && recs[0].S3.Object.URLDecodedKey != tt.wantKey {
				t.Errorf("key = %q", recs[0].S3.Object.URLDecodedKey)
			}
		})
	}
}
