// Package dlq writes to and reads the dead-letter queue: an SQS queue that
// keeps, for 14 days, what failed and will not be retried automatically,
// until the user looks at it (radar dlq) or sends it again (radar redrive).
package dlq

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Queue is one SQS queue, by URL.
type Queue struct {
	Client *sqs.Client
	URL    string
}

// Put sends v as one JSON message.
func (q Queue) Put(ctx context.Context, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = q.Client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: &q.URL, MessageBody: aws.String(string(body))})
	return err
}

// Message is one message read back, with the handle Delete needs.
type Message struct {
	ID, Body, Handle string
}

// Peek reads up to max messages. They stay in the queue: they come back
// after hide seconds unless deleted.
func (q Queue) Peek(ctx context.Context, max int, hide int32) ([]Message, error) {
	var out []Message
	for len(out) < max {
		resp, err := q.Client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: &q.URL, MaxNumberOfMessages: int32(min(10, max-len(out))), VisibilityTimeout: hide, WaitTimeSeconds: 1,
		})
		if err != nil {
			return out, fmt.Errorf("read dead letters: %w", err)
		}
		if len(resp.Messages) == 0 {
			break
		}
		for _, m := range resp.Messages {
			out = append(out, Message{ID: aws.ToString(m.MessageId), Body: aws.ToString(m.Body), Handle: aws.ToString(m.ReceiptHandle)})
		}
	}
	return out, nil
}

// Delete removes a message read with Peek.
func (q Queue) Delete(ctx context.Context, m Message) error {
	_, err := q.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: &q.URL, ReceiptHandle: &m.Handle})
	return err
}
