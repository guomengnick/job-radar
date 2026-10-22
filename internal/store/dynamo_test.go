package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// fakeDynamo records requests and answers just enough to drive the store's
// own logic: conditional puts, paged queries and unprocessed batch items.
// It does not evaluate expressions; the tests assert on them instead.
type fakeDynamo struct {
	mu          sync.Mutex
	keys        map[string]bool // job keys already stored, for attribute_not_exists
	pages       [][]map[string]types.AttributeValue
	queries     []*dynamodb.QueryInput
	puts        []*dynamodb.PutItemInput
	updates     []*dynamodb.UpdateItemInput
	batchCalls  int
	unprocessed int // items to hand back unprocessed on the first batch call
	written     map[string]int
	putErr      error
	gets        []*dynamodb.GetItemInput
	item        map[string]types.AttributeValue // what GetItem answers
}

func newFake() *fakeDynamo {
	return &fakeDynamo{keys: map[string]bool{}, written: map[string]int{}}
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, in)
	page := 0
	if v, ok := in.ExclusiveStartKey["page"].(*types.AttributeValueMemberN); ok {
		page, _ = strconv.Atoi(v.Value)
	}
	out := &dynamodb.QueryOutput{}
	if page < len(f.pages) {
		out.Items = f.pages[page]
	}
	if page+1 < len(f.pages) {
		out.LastEvaluatedKey = map[string]types.AttributeValue{"page": &types.AttributeValueMemberN{Value: strconv.Itoa(page + 1)}}
	}
	return out, nil
}

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, in)
	return &dynamodb.GetItemOutput{Item: f.item}, nil
}

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, in)
	if f.putErr != nil {
		return nil, f.putErr
	}
	if in.ConditionExpression != nil {
		key := in.Item["key"].(*types.AttributeValueMemberS).Value
		if f.keys[key] {
			return nil, &types.ConditionalCheckFailedException{Message: aws.String("exists")}
		}
		f.keys[key] = true
	}
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDynamo) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, in)
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *fakeDynamo) BatchWriteItem(_ context.Context, in *dynamodb.BatchWriteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchCalls++
	out := &dynamodb.BatchWriteItemOutput{UnprocessedItems: map[string][]types.WriteRequest{}}
	for table, reqs := range in.RequestItems {
		if len(reqs) > 25 {
			return nil, errors.New("ValidationException: more than 25 requests")
		}
		if f.unprocessed > 0 {
			n := min(f.unprocessed, len(reqs))
			out.UnprocessedItems[table] = reqs[len(reqs)-n:]
			reqs = reqs[:len(reqs)-n]
			f.unprocessed = 0
		}
		f.written[table] += len(reqs)
	}
	return out, nil
}

func s(av types.AttributeValue) string {
	if v, ok := av.(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func TestDynamoInsertJobIfNew(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	d := NewDynamo(f, "boards", "jobs", "runs")
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	isNew, err := d.InsertJobIfNew(ctx, JobRecord{Key: "lever#a#1", State: JobNew, FirstSeen: now})
	if err != nil || !isNew {
		t.Fatalf("first insert: new=%v err=%v", isNew, err)
	}
	put := f.puts[0]
	if *put.TableName != "jobs" || *put.ConditionExpression != "attribute_not_exists(#k)" || put.ExpressionAttributeNames["#k"] != "key" {
		t.Fatalf("put is not conditional on the key: %+v", put)
	}
	// pending-index (infra/dynamodb.tf) is keyed on pending and first_seen,
	// both declared as strings; a key attribute of another type fails the write.
	if s(put.Item["pending"]) != string(JobNew) || s(put.Item["first_seen"]) != "2026-10-12T08:00:00Z" {
		t.Fatalf("index key attributes: pending=%#v first_seen=%#v", put.Item["pending"], put.Item["first_seen"])
	}

	again, err := d.InsertJobIfNew(ctx, JobRecord{Key: "lever#a#1", State: JobNew, FirstSeen: now})
	if err != nil || again {
		t.Fatalf("a failed condition means not new: new=%v err=%v", again, err)
	}

	d.InsertJobIfNew(ctx, JobRecord{Key: "lever#a#2", State: JobSeen, FirstSeen: now})
	if _, ok := f.puts[2].Item["pending"]; ok {
		t.Fatal("a job below the threshold must not carry pending, or the sparse index fills up")
	}

	f.putErr = errors.New("throttled")
	if _, err := d.InsertJobIfNew(ctx, JobRecord{Key: "lever#a#3", State: JobNew}); err == nil {
		t.Fatal("other errors must be returned, not read as not-new")
	}
}

func TestDynamoListBoardsReadsEveryPage(t *testing.T) {
	f := newFake()
	for p := 0; p < 3; p++ {
		var page []map[string]types.AttributeValue
		for i := 0; i < 2; i++ {
			item, err := attributevalue.MarshalMap(Board{ATS: "greenhouse", Slug: "b" + strconv.Itoa(p*2+i), Status: BoardOK})
			if err != nil {
				t.Fatal(err)
			}
			page = append(page, item)
		}
		f.pages = append(f.pages, page)
	}
	boards, err := NewDynamo(f, "boards", "jobs", "runs").ListBoards(context.Background(), "greenhouse")
	if err != nil {
		t.Fatal(err)
	}
	if len(boards) != 6 || len(f.queries) != 3 || boards[5].Slug != "b5" || boards[0].Status != BoardOK {
		t.Fatalf("boards=%d queries=%d last=%+v", len(boards), len(f.queries), boards[len(boards)-1])
	}
	if q := f.queries[0]; *q.KeyConditionExpression != "ats = :a" || s(q.ExpressionAttributeValues[":a"]) != "greenhouse" {
		t.Fatalf("query = %+v", q)
	}
}

func TestDynamoSaveBoardsRetriesUnprocessed(t *testing.T) {
	f := newFake()
	f.unprocessed = 3
	var boards []Board
	for i := 0; i < 30; i++ {
		boards = append(boards, Board{ATS: "lever", Slug: "s" + strconv.Itoa(i)})
	}
	if err := NewDynamo(f, "boards", "jobs", "runs").SaveBoards(context.Background(), boards); err != nil {
		t.Fatal(err)
	}
	// 30 items: a chunk of 25 (3 handed back, then retried) and a chunk of 5.
	if f.written["boards"] != 30 || f.batchCalls != 3 {
		t.Fatalf("written=%d calls=%d", f.written["boards"], f.batchCalls)
	}
}

func TestDynamoRunsAndNotified(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	d := NewDynamo(f, "boards", "jobs", "runs")
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

	if err := d.SaveRun(ctx, Run{ATS: "ashby", Task: "scan", StartedAt: start, Status: "ok", Outcomes: map[string]int{"ok": 2}}); err != nil {
		t.Fatal(err)
	}
	item := f.puts[0].Item
	ttl, _ := item["expires_at"].(*types.AttributeValueMemberN)
	if s(item["pk"]) != "run" || s(item["sk"]) != "2026-10-12T00:00:00Z#ashby#scan" ||
		ttl == nil || ttl.Value != strconv.FormatInt(start.Add(90*24*time.Hour).Unix(), 10) {
		t.Fatalf("run item keys/TTL: pk=%v sk=%v expires_at=%#v", item["pk"], item["sk"], item["expires_at"])
	}

	f.pages = [][]map[string]types.AttributeValue{{item}}
	runs, err := d.RunsSince(ctx, start.Add(-time.Hour))
	if err != nil || len(runs) != 1 || runs[0].ATS != "ashby" || runs[0].Outcomes["ok"] != 2 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	if q := f.queries[0]; s(q.ExpressionAttributeValues[":s"]) != "2026-10-11T23:00:00Z" {
		t.Fatalf("since bound = %v", q.ExpressionAttributeValues[":s"])
	}

	if err := d.MarkNotified(ctx, []string{"lever#a#1"}, start); err != nil {
		t.Fatal(err)
	}
	u := f.updates[0]
	if !strings.Contains(*u.UpdateExpression, "REMOVE pending") || u.ExpressionAttributeNames["#s"] != "state" {
		t.Fatalf("a notified job must leave the pending index: %s %v", *u.UpdateExpression, u.ExpressionAttributeNames)
	}
}

func TestDynamoSaveAssessment(t *testing.T) {
	f := newFake()
	d := NewDynamo(f, "boards", "jobs", "runs")
	a := Assessment{Key: "assess#104#aaa01", URL: "https://www.104.com.tw/job/aaa01", Score: 82, Detail: `{"score":82}`}
	if err := d.SaveAssessment(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	p := f.puts[0]
	if *p.TableName != "jobs" || s(p.Item["key"]) != "assess#104#aaa01" || p.ConditionExpression != nil {
		t.Fatalf("put = table %s key %v condition %v", *p.TableName, p.Item["key"], p.ConditionExpression)
	}
	if _, ok := p.Item["pending"]; ok {
		t.Error("an assessment must stay out of the pending index")
	}
}

func TestDynamoJobsSince(t *testing.T) {
	f := newFake()
	item, err := attributevalue.MarshalMap(JobRecord{Key: "104#aaa01", ATS: "104", Title: "後端工程師", State: JobSeen})
	if err != nil {
		t.Fatal(err)
	}
	f.pages = [][]map[string]types.AttributeValue{{item}, {item}}
	since := time.Date(2026, 10, 21, 16, 0, 0, 250, time.FixedZone("Taipei", 8*3600))
	jobs, err := NewDynamo(f, "boards", "jobs", "runs").JobsSince(context.Background(), "104", since)
	if err != nil || len(jobs) != 2 || jobs[0].Title != "後端工程師" || jobs[0].State != JobSeen {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	q := f.queries[0]
	if *q.IndexName != "source-index" || *q.KeyConditionExpression != "ats = :a AND first_seen >= :s" ||
		s(q.ExpressionAttributeValues[":a"]) != "104" || s(q.ExpressionAttributeValues[":s"]) != "2026-10-21T08:00:00.000000000Z" {
		t.Fatalf("query = index %v, %s, %v", q.IndexName, *q.KeyConditionExpression, q.ExpressionAttributeValues)
	}
}

// The index compares first_seen as strings, so the bound must sort like the
// times it stands for against every form the marshaller writes.
func TestTimeBoundSortsLikeTime(t *testing.T) {
	bound := time.Date(2026, 10, 21, 16, 0, 0, 0, time.UTC)
	for _, ns := range []int{0, 1, 250_000_000, 999_999_999} {
		for _, sec := range []int{-1, 0, 1} {
			at := bound.Add(time.Duration(sec)*time.Second + time.Duration(ns))
			av, err := attributevalue.Marshal(at)
			if err != nil {
				t.Fatal(err)
			}
			stored := s(av)
			if got, want := stored >= timeBound(bound), !at.Before(bound); got != want {
				t.Errorf("%s >= %s is %v, but the time is at or after the bound: %v", stored, timeBound(bound), got, want)
			}
		}
	}
}

func TestDynamoBatches(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	d := NewDynamo(f, "boards", "jobs", "runs")
	at := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	b := Batch{Key: "batch#msgbatch_1", Pending: BatchPending, SubmittedAt: at, BatchID: "msgbatch_1",
		Items: []BatchItem{{ID: "p0", Key: "104#aaa01", URL: "https://www.104.com.tw/job/aaa01", Title: "後端"}}}
	if err := d.SaveBatch(ctx, b); err != nil {
		t.Fatal(err)
	}
	item := f.puts[0].Item
	// pending-index is keyed on pending and first_seen.
	if s(item["pending"]) != "batch" || s(item["first_seen"]) != "2026-10-23T01:00:00Z" {
		t.Fatalf("index keys: %v %v", item["pending"], item["first_seen"])
	}
	if _, ok := item["ats"]; ok {
		t.Error("a batch must stay out of the source index")
	}

	f.pages = [][]map[string]types.AttributeValue{{item}}
	open, err := d.OpenBatches(ctx)
	if err != nil || len(open) != 1 || open[0].BatchID != "msgbatch_1" || open[0].Items[0].Key != "104#aaa01" {
		t.Fatalf("open=%+v err=%v", open, err)
	}
	if q := f.queries[0]; *q.IndexName != "pending-index" || s(q.ExpressionAttributeValues[":b"]) != "batch" {
		t.Fatalf("query = %+v", q)
	}
	if err := d.CloseBatch(ctx, b.Key, at); err != nil {
		t.Fatal(err)
	}
	if u := f.updates[0]; !strings.Contains(*u.UpdateExpression, "REMOVE pending") || s(u.Key["key"]) != b.Key {
		t.Fatalf("close = %s", *u.UpdateExpression)
	}
}

func TestDynamoAssessmentOf(t *testing.T) {
	f := newFake()
	d := NewDynamo(f, "boards", "jobs", "runs")
	if _, ok, err := d.AssessmentOf(context.Background(), "assess#104#aaa01"); ok || err != nil {
		t.Fatalf("missing item: ok=%v err=%v", ok, err)
	}
	f.item, _ = attributevalue.MarshalMap(Assessment{Key: "assess#104#aaa01", Score: 70})
	a, ok, err := d.AssessmentOf(context.Background(), "assess#104#aaa01")
	if !ok || err != nil || a.Score != 70 || s(f.gets[1].Key["key"]) != "assess#104#aaa01" {
		t.Fatalf("a=%+v ok=%v err=%v", a, ok, err)
	}
}
