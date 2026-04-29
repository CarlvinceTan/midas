package tools

import (
	"context"
	"fmt"

	"github.com/carlvincetan/polymux/internal/midas/browser"
)

type Service interface {
	Specs() []Spec
	Execute(context.Context, string, map[string]any) (Result, error)
}

type BoundService struct {
	bctx     *browser.Context
	registry Registry
	cacheSvc *CacheService
}

func NewService(bctx *browser.Context) *BoundService {
	return &BoundService{
		bctx:     bctx,
		registry: NewRegistry(),
	}
}

func NewServiceWithCache(bctx *browser.Context, cacheSvc *CacheService) *BoundService {
	r := NewRegistry()
	RegisterCacheTools(&r, cacheSvc)
	return &BoundService{
		bctx:     bctx,
		registry: r,
		cacheSvc: cacheSvc,
	}
}

func (s *BoundService) Specs() []Spec {
	if s == nil {
		return nil
	}
	return s.registry.Specs()
}

func (s *BoundService) Execute(ctx context.Context, name string, input map[string]any) (Result, error) {
	if s == nil {
		return Result{}, fmt.Errorf("tool service is nil")
	}
	return s.registry.Execute(ctx, s.bctx, name, input)
}

func (s *BoundService) CacheService() *CacheService {
	return s.cacheSvc
}
