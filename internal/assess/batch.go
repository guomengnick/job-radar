package assess

import (
	"context"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// BatchItem is one page of a Message Batch. ID names its result: letters,
// digits, "_" and "-", at most 64, unique within the batch.
type BatchItem struct {
	ID   string
	Page Page
}

// Result is what became of one batch item: an assessment, or Err.
type Result struct {
	ID         string
	Assessment Assessment
	Usage      Usage
	Err        error
}

// Submit sends the pages as one Message Batch and returns its ID. A batch
// costs half the price of single calls; Anthropic processes it within a
// day, usually within the hour, and Ended says when it is done.
func (c Client) Submit(ctx context.Context, profile string, items []BatchItem) (string, error) {
	reqs := make([]anthropic.MessageBatchNewParamsRequest, len(items))
	for i, it := range items {
		r := c.request(profile, it.Page)
		reqs[i] = anthropic.MessageBatchNewParamsRequest{
			CustomID: it.ID,
			Params: anthropic.MessageBatchNewParamsRequestParams{
				Model: anthropic.Model(c.Model), MaxTokens: MaxTokens, System: r.system, Tools: r.tools,
				ToolChoice: r.choice, OutputConfig: r.output, Messages: r.messages,
			},
		}
	}
	api := c.api()
	b, err := api.Messages.Batches.New(ctx, anthropic.MessageBatchNewParams{Requests: reqs})
	if err != nil {
		return "", fmt.Errorf("anthropic: submit batch: %w", err)
	}
	return b.ID, nil
}

// Ended reports whether a batch has finished: every request succeeded,
// failed, or expired.
func (c Client) Ended(ctx context.Context, batchID string) (bool, error) {
	api := c.api()
	b, err := api.Messages.Batches.Get(ctx, batchID, anthropic.MessageBatchGetParams{})
	if err != nil {
		return false, fmt.Errorf("anthropic: batch %s: %w", batchID, err)
	}
	return b.ProcessingStatus == anthropic.MessageBatchProcessingStatusEnded, nil
}

// Results reads every result of an ended batch, in no particular order.
func (c Client) Results(ctx context.Context, batchID string) ([]Result, error) {
	api := c.api()
	stream := api.Messages.Batches.ResultsStreaming(ctx, batchID, anthropic.MessageBatchResultsParams{})
	defer stream.Close()
	var out []Result
	for stream.Next() {
		r := stream.Current()
		res := Result{ID: r.CustomID}
		switch v := r.Result.AsAny().(type) {
		case anthropic.MessageBatchSucceededResult:
			res.Assessment, res.Usage, res.Err = Read(&v.Message)
		case anthropic.MessageBatchErroredResult:
			res.Err = fmt.Errorf("anthropic: %s", v.Error.Error.Message)
		case anthropic.MessageBatchExpiredResult:
			res.Err = errors.New("anthropic: not processed within 24 hours")
		default:
			res.Err = fmt.Errorf("anthropic: result %q", r.Result.Type)
		}
		out = append(out, res)
	}
	if err := stream.Err(); err != nil {
		return out, fmt.Errorf("anthropic: batch %s results: %w", batchID, err)
	}
	return out, nil
}
