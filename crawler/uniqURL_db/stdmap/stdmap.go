package stdmap

import (
	"context"
	"crawler/uniqURL_db"
)

type url = string

type stdMAP struct {
	hash map[url]int32
}

func New() uniqURL_db.Parsed {
	return &stdMAP{make(map[url]int32)}
}

func (m stdMAP) Get(ctx context.Context, url url) (depth int32, exists bool, err error) {
	depth, urlVisited := m.hash[url]

	return depth, urlVisited, nil
}

func (m stdMAP) Save(ctx context.Context, url url, depth int32) error {
	m.hash[url] = depth

	return nil
}

func (m stdMAP) Close() error {
	return nil
}

