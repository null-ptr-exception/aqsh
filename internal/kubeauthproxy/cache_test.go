package proxy

import (
	"context"
	"testing"
	"time"
)

// countingReviewer tracks how many times Review is called.
type countingReviewer struct {
	inner TokenReviewer
	count int
}

func (r *countingReviewer) Review(ctx context.Context, token string) (*TokenReviewResponse, error) {
	r.count++
	return r.inner.Review(ctx, token)
}

func TestCachedTokenReviewer_CacheHit(t *testing.T) {
	inner := &countingReviewer{inner: authenticatedReviewer("user", nil, nil)}
	cached := NewCachedTokenReviewer(inner)

	ctx := context.Background()
	cached.Review(ctx, "token-a")
	cached.Review(ctx, "token-a")

	if inner.count != 1 {
		t.Errorf("inner reviewer called %d times, want 1", inner.count)
	}
}

func TestCachedTokenReviewer_CacheExpiry(t *testing.T) {
	inner := &countingReviewer{inner: authenticatedReviewer("user", nil, nil)}
	cached := NewCachedTokenReviewer(inner)

	ctx := context.Background()
	cached.Review(ctx, "token-a")

	// Manually expire the entry
	if entry, ok := cached.cache.Load("token-a"); ok {
		e := entry.(*cacheEntry)
		e.expiry = time.Now().Add(-1 * time.Second)
	}

	cached.Review(ctx, "token-a")

	if inner.count != 2 {
		t.Errorf("inner reviewer called %d times, want 2", inner.count)
	}
}

func TestCachedTokenReviewer_UnauthenticatedNotCached(t *testing.T) {
	inner := &countingReviewer{inner: unauthenticatedReviewer()}
	cached := NewCachedTokenReviewer(inner)

	ctx := context.Background()
	cached.Review(ctx, "bad-token")
	cached.Review(ctx, "bad-token")

	if inner.count != 2 {
		t.Errorf("inner reviewer called %d times, want 2", inner.count)
	}
}

func TestCachedTokenReviewer_ErrorNotCached(t *testing.T) {
	inner := &countingReviewer{inner: errorReviewer("connection refused")}
	cached := NewCachedTokenReviewer(inner)

	ctx := context.Background()
	cached.Review(ctx, "token-a")
	cached.Review(ctx, "token-a")

	if inner.count != 2 {
		t.Errorf("inner reviewer called %d times, want 2", inner.count)
	}
}
