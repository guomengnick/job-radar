package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Dynamo backs Store with three on-demand tables (see infra/dynamodb.tf):
//
//	boards: PK ats, SK slug
//	jobs:   PK key; sparse GSI "pending-index" (pending, first_seen) = unsent jobs;
//	        GSI "source-index" (ats, first_seen) = every job by source and time
//	runs:   PK pk="run", SK sk="<started_at>#<ats>#<task>"; TTL on expires_at
type Dynamo struct {
	db                 DynamoAPI
	boards, jobs, runs string
	runTTL             time.Duration
}

// DynamoAPI is the part of *dynamodb.Client the store uses, so tests can
// check the requests it sends without an AWS account.
type DynamoAPI interface {
	dynamodb.QueryAPIClient
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	BatchWriteItem(context.Context, *dynamodb.BatchWriteItemInput, ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
}

const (
	pendingIndex = "pending-index"
	sourceIndex  = "source-index"
)

func NewDynamo(db DynamoAPI, boardsTable, jobsTable, runsTable string) *Dynamo {
	return &Dynamo{db: db, boards: boardsTable, jobs: jobsTable, runs: runsTable, runTTL: 90 * 24 * time.Hour}
}

func (d *Dynamo) ListBoards(ctx context.Context, ats string) ([]Board, error) {
	p := dynamodb.NewQueryPaginator(d.db, &dynamodb.QueryInput{
		TableName:                 &d.boards,
		KeyConditionExpression:    aws.String("ats = :a"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":a": &types.AttributeValueMemberS{Value: ats}},
	})
	var out []Board
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list boards: %w", err)
		}
		var bs []Board
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &bs); err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	return out, nil
}

func (d *Dynamo) SaveBoards(ctx context.Context, boards []Board) error {
	reqs := make([]types.WriteRequest, 0, len(boards))
	for _, b := range boards {
		item, err := attributevalue.MarshalMap(b)
		if err != nil {
			return err
		}
		reqs = append(reqs, types.WriteRequest{PutRequest: &types.PutRequest{Item: item}})
	}
	return d.batchWrite(ctx, d.boards, reqs)
}

func (d *Dynamo) AddBoards(ctx context.Context, ats string, slugs []string) (int, error) {
	existing, err := d.ListBoards(ctx, ats)
	if err != nil {
		return 0, err
	}
	known := make(map[string]bool, len(existing))
	for _, b := range existing {
		known[b.Slug] = true
	}
	var fresh []Board
	for _, s := range slugs {
		if !known[s] {
			known[s] = true
			fresh = append(fresh, Board{ATS: ats, Slug: s, Status: BoardUnknown})
		}
	}
	return len(fresh), d.SaveBoards(ctx, fresh)
}

// batchWrite sends 25 items per request and retries what DynamoDB hands back
// as unprocessed (throttling) with a growing pause.
func (d *Dynamo) batchWrite(ctx context.Context, table string, reqs []types.WriteRequest) error {
	for start := 0; start < len(reqs); start += 25 {
		chunk := reqs[start:min(start+25, len(reqs))]
		pending := map[string][]types.WriteRequest{table: chunk}
		for attempt := 0; len(pending[table]) > 0; attempt++ {
			if attempt > 0 {
				if attempt > 8 {
					return fmt.Errorf("batch write %s: %d items still unprocessed", table, len(pending[table]))
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(50<<attempt) * time.Millisecond):
				}
			}
			out, err := d.db.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: pending})
			if err != nil {
				return fmt.Errorf("batch write %s: %w", table, err)
			}
			pending = out.UnprocessedItems
		}
	}
	return nil
}

func (d *Dynamo) InsertJobIfNew(ctx context.Context, j JobRecord) (bool, error) {
	if j.State == JobNew {
		j.Pending = string(JobNew)
	}
	item, err := attributevalue.MarshalMap(j)
	if err != nil {
		return false, err
	}
	// The conditional put is both the "is it new?" check and the dedupe:
	// two workers racing on the same posting cannot both insert it.
	_, err = d.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                &d.jobs,
		Item:                     item,
		ConditionExpression:      aws.String("attribute_not_exists(#k)"),
		ExpressionAttributeNames: map[string]string{"#k": "key"},
	})
	var ccf *types.ConditionalCheckFailedException
	if errors.As(err, &ccf) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert job: %w", err)
	}
	return true, nil
}

func (d *Dynamo) PendingJobs(ctx context.Context) ([]JobRecord, error) {
	p := dynamodb.NewQueryPaginator(d.db, &dynamodb.QueryInput{
		TableName:                 &d.jobs,
		IndexName:                 aws.String(pendingIndex),
		KeyConditionExpression:    aws.String("pending = :n"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":n": &types.AttributeValueMemberS{Value: string(JobNew)}},
	})
	var out []JobRecord
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("pending jobs: %w", err)
		}
		var js []JobRecord
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &js); err != nil {
			return nil, err
		}
		out = append(out, js...)
	}
	return out, nil
}

func (d *Dynamo) JobsSince(ctx context.Context, ats string, since time.Time) ([]JobRecord, error) {
	p := dynamodb.NewQueryPaginator(d.db, &dynamodb.QueryInput{
		TableName:              &d.jobs,
		IndexName:              aws.String(sourceIndex),
		KeyConditionExpression: aws.String("ats = :a AND first_seen >= :s"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":a": &types.AttributeValueMemberS{Value: ats},
			":s": &types.AttributeValueMemberS{Value: timeBound(since)},
		},
	})
	var out []JobRecord
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("jobs since: %w", err)
		}
		var js []JobRecord
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &js); err != nil {
			return nil, err
		}
		out = append(out, js...)
	}
	return out, nil
}

// timeBound writes since for comparison with stored times. Those are
// RFC 3339 strings in UTC that drop trailing zeros of the fraction
// ("…:05Z", "…:05.25Z"), and as strings "…:05Z" sorts after "…:05.25Z".
// A bound with all nine fraction digits sorts correctly against both forms;
// since is cut to the second first.
func timeBound(since time.Time) string {
	return since.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05.000000000Z07:00")
}

func (d *Dynamo) MarkNotified(ctx context.Context, keys []string, at time.Time) error {
	ts, err := attributevalue.Marshal(at)
	if err != nil {
		return err
	}
	for _, k := range keys {
		_, err := d.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:                &d.jobs,
			Key:                      map[string]types.AttributeValue{"key": &types.AttributeValueMemberS{Value: k}},
			UpdateExpression:         aws.String("SET #s = :n, notified_at = :t REMOVE pending"),
			ExpressionAttributeNames: map[string]string{"#s": "state"},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":n": &types.AttributeValueMemberS{Value: string(JobNotified)}, ":t": ts,
			},
		})
		if err != nil {
			return fmt.Errorf("mark notified %s: %w", k, err)
		}
	}
	return nil
}

type runItem struct {
	PK        string `dynamodbav:"pk"`
	SK        string `dynamodbav:"sk"`
	ExpiresAt int64  `dynamodbav:"expires_at"`
	Run
}

func (d *Dynamo) SaveRun(ctx context.Context, r Run) error {
	item, err := attributevalue.MarshalMap(runItem{
		PK: "run", SK: r.StartedAt.UTC().Format(time.RFC3339) + "#" + r.ATS + "#" + r.Task,
		ExpiresAt: r.StartedAt.Add(d.runTTL).Unix(), Run: r,
	})
	if err != nil {
		return err
	}
	_, err = d.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: &d.runs, Item: item})
	return err
}

// SaveAssessment puts the judgement in the jobs table under its own key
// prefix. It has no pending attribute, so the digest never sees it.
func (d *Dynamo) SaveAssessment(ctx context.Context, a Assessment) error {
	item, err := attributevalue.MarshalMap(a)
	if err != nil {
		return err
	}
	_, err = d.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: &d.jobs, Item: item})
	if err != nil {
		return fmt.Errorf("save assessment: %w", err)
	}
	return nil
}

func (d *Dynamo) AssessmentOf(ctx context.Context, key string) (Assessment, bool, error) {
	out, err := d.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &d.jobs,
		Key:       map[string]types.AttributeValue{"key": &types.AttributeValueMemberS{Value: key}},
	})
	if err != nil {
		return Assessment{}, false, fmt.Errorf("get assessment: %w", err)
	}
	if out.Item == nil {
		return Assessment{}, false, nil
	}
	var a Assessment
	err = attributevalue.UnmarshalMap(out.Item, &a)
	return a, err == nil, err
}

// SaveBatch puts the batch in the jobs table under its own key prefix. It
// has no ats, so the source index never lists it.
func (d *Dynamo) SaveBatch(ctx context.Context, b Batch) error {
	item, err := attributevalue.MarshalMap(b)
	if err != nil {
		return err
	}
	if _, err := d.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: &d.jobs, Item: item}); err != nil {
		return fmt.Errorf("save batch: %w", err)
	}
	return nil
}

func (d *Dynamo) OpenBatches(ctx context.Context) ([]Batch, error) {
	p := dynamodb.NewQueryPaginator(d.db, &dynamodb.QueryInput{
		TableName:                 &d.jobs,
		IndexName:                 aws.String(pendingIndex),
		KeyConditionExpression:    aws.String("pending = :b"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":b": &types.AttributeValueMemberS{Value: BatchPending}},
	})
	var out []Batch
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("open batches: %w", err)
		}
		var bs []Batch
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &bs); err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	return out, nil
}

func (d *Dynamo) CloseBatch(ctx context.Context, key string, at time.Time) error {
	ts, err := attributevalue.Marshal(at)
	if err != nil {
		return err
	}
	_, err = d.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &d.jobs,
		Key:                       map[string]types.AttributeValue{"key": &types.AttributeValueMemberS{Value: key}},
		UpdateExpression:          aws.String("SET closed_at = :t REMOVE pending"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":t": ts},
	})
	if err != nil {
		return fmt.Errorf("close batch %s: %w", key, err)
	}
	return nil
}

func (d *Dynamo) RunsSince(ctx context.Context, since time.Time) ([]Run, error) {
	p := dynamodb.NewQueryPaginator(d.db, &dynamodb.QueryInput{
		TableName:              &d.runs,
		KeyConditionExpression: aws.String("pk = :p AND sk >= :s"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":p": &types.AttributeValueMemberS{Value: "run"},
			":s": &types.AttributeValueMemberS{Value: since.UTC().Format(time.RFC3339)},
		},
	})
	var out []Run
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("runs since: %w", err)
		}
		var items []runItem
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &items); err != nil {
			return nil, err
		}
		for _, it := range items {
			out = append(out, it.Run)
		}
	}
	return out, nil
}
