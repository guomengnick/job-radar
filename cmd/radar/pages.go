package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// s3Pages reads and writes the kept pages in the pages bucket
// (job-radar-pages-<account>): app.PageSource and app.PageArchive.
type s3Pages struct {
	client *s3.Client
	bucket string
}

func newS3Pages(ctx context.Context, cfg aws.Config) (s3Pages, error) {
	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return s3Pages{}, fmt.Errorf("who am I (run aws login?): %w", err)
	}
	return s3Pages{client: s3.NewFromConfig(cfg), bucket: "job-radar-pages-" + aws.ToString(id.Account)}, nil
}

func (p s3Pages) Load(ctx context.Context, name string, v any) error {
	out, err := p.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &p.bucket, Key: &name})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// List returns the names under prefix, oldest first: the names are
// timestamps, so sorting them sorts by time.
func (p s3Pages) List(ctx context.Context, prefix string) ([]string, error) {
	var names []string
	pager := s3.NewListObjectsV2Paginator(p.client, &s3.ListObjectsV2Input{Bucket: &p.bucket, Prefix: &prefix})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			names = append(names, aws.ToString(o.Key))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (p s3Pages) Save(ctx context.Context, name string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = p.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &p.bucket, Key: &name, Body: bytes.NewReader(body), ContentType: aws.String("application/json"),
	})
	return err
}
