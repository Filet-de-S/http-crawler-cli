package uniqURL_db

import "context"

type Parsed interface {
	Get(ctx context.Context, url URL) (depth int32, exists bool, err error)
	Save(ctx context.Context, url URL, depth int32) error
	Close() error
}

type URL = string
