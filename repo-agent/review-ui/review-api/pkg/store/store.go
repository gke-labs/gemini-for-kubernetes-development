package store

import "context"

type Store interface {
	SaveRepo(ctx context.Context, namespace, name, url string) error
	DeleteRepo(ctx context.Context, namespace, name string) error
	ListRepos(ctx context.Context, namespace string) ([]string, error)
}
