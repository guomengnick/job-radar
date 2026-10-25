// Package pages keeps job pages in the S3 pages bucket
// (job-radar-pages-<account>): each batch as sent under batches/, each
// judged page under jd/. S3 is both an app.PageArchive and an
// app.PageSource.
package pages

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

// S3 reads and writes JSON objects in one bucket.
type S3 struct {
	Client *s3.Client
	Bucket string
}

// ForAccount names the bucket after the account the credentials belong to,
// for the radar command on a laptop; a Lambda gets the name from Terraform.
func ForAccount(ctx context.Context, cfg aws.Config) (S3, error) {
	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return S3{}, fmt.Errorf("who am I (run aws login?): %w", err)
	}
	return S3{Client: s3.NewFromConfig(cfg), Bucket: "job-radar-pages-" + aws.ToString(id.Account)}, nil
}

func (p S3) Load(ctx context.Context, name string, v any) error {
	out, err := p.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: &p.Bucket, Key: &name})
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
func (p S3) List(ctx context.Context, prefix string) ([]string, error) {
	var names []string
	pager := s3.NewListObjectsV2Paginator(p.Client, &s3.ListObjectsV2Input{Bucket: &p.Bucket, Prefix: &prefix})
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

func (p S3) Save(ctx context.Context, name string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = p.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &p.Bucket, Key: &name, Body: bytes.NewReader(body), ContentType: aws.String("application/json"),
	})
	return err
}
