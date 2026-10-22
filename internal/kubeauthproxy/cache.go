package proxy

import (
	"context"
	"sync"
	"time"
)

const cacheTTL = 60 * time.Second

type cacheEntry struct {
	response *TokenReviewResponse
	expiry   time.Time
}

// CachedTokenReviewer wraps a TokenReviewer with an in-memory cache.
// Only successful authentications are cached.
type CachedTokenReviewer struct {
	inner TokenReviewer
	cache sync.Map
}

func NewCachedTokenReviewer(inner TokenReviewer) *CachedTokenReviewer {
	return &CachedTokenReviewer{inner: inner}
}

func (c *CachedTokenReviewer) Review(ctx context.Context, token string) (*TokenReviewResponse, error) {
	if entry, ok := c.cache.Load(token); ok {
		e := entry.(*cacheEntry)
		if time.Now().Before(e.expiry) {
			return e.response, nil
		}
		c.cache.Delete(token)
	}

	result, err := c.inner.Review(ctx, token)
	if err != nil {
		return nil, err
	}

	if result.Status.Authenticated {
		c.cache.Store(token, &cacheEntry{
			response: result,
			expiry:   time.Now().Add(cacheTTL),
		})
	}

	return result, nil
}
