package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewCacheExpiryRotationAndFailure(t *testing.T) {
	var calls, state atomic.Int32
	var clock atomic.Int64
	var reviewer atomic.Value
	reviewer.Store("reviewer-one")
	endpoint, _ := testCluster(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+reviewer.Load().(string) {
			t.Error("stale reviewer credential")
		}
		switch state.Load() {
		case 1:
			w.Write([]byte(`{"status":{"authenticated":false}}`))
		case 2:
			w.WriteHeader(503)
		default:
			successReview(w)
		}
	})
	k, err := New(Config{Audiences: []string{"aqsh"}, CacheTTL: 10 * time.Second, TokenReview: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	check := func(wantCalls int32, wantErr error) Principal {
		t.Helper()
		p, err := k.Authenticate(context.Background(), "caller")
		if !errors.Is(err, wantErr) || calls.Load() != wantCalls {
			t.Fatalf("calls=%d want=%d err=%v want=%v", calls.Load(), wantCalls, err, wantErr)
		}
		return p
	}
	p := check(1, nil)
	p.Groups[0] = "forged"
	clock.Store(9)
	state.Store(2)
	p = check(1, nil) // A live entry also tolerates a brief endpoint outage.
	if !slices.Equal(p.Groups, []string{"team"}) {
		t.Fatal("caller mutated cached groups")
	}
	p.Groups[0] = "also-forged"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := k.Authenticate(ctx, "caller"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("cancelled cache hit succeeded")
	}
	if err := os.WriteFile(endpoint.TokenFile, []byte("reviewer-two"), 0600); err != nil {
		t.Fatal(err)
	}
	reviewer.Store("reviewer-two")
	clock.Store(10) // The hit at t=9 must not extend the deadline.
	check(2, ErrUnavailable)
	state.Store(1)
	check(3, ErrUnauthenticated)
	state.Store(0)
	p = check(4, nil) // Denials and backend errors must not be cached.
	if !slices.Equal(p.Groups, []string{"team"}) {
		t.Fatal("cached groups contaminated review")
	}
	clock.Store(19)
	check(4, nil)
	clock.Store(20)
	check(5, nil)
	if _, ok := k.cache[sha256.Sum256([]byte("caller"))]; !ok {
		t.Fatal("cache is not keyed by token digest")
	}
}

func TestReviewCacheBoundedAndIsolated(t *testing.T) {
	var clock atomic.Int64
	endpoint, _ := testCluster(t, func(w http.ResponseWriter, r *http.Request) {
		var review tokenReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{
			"authenticated": true, "audiences": []string{"aqsh"},
			"user": map[string]any{"username": review.Spec.Token, "groups": []string{"team"}},
		}})
	})
	k, err := New(Config{Audiences: []string{"aqsh"}, CacheTTL: time.Second, TokenReview: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	for i := 0; i < maxCachedReviews+2; i++ {
		token := fmt.Sprint(i)
		p, err := k.Authenticate(context.Background(), token)
		if err != nil || p.Identity != token {
			t.Fatalf("token identity mixed up: %v", err)
		}
	}
	if len(k.cache) != maxCachedReviews {
		t.Fatalf("unbounded cache: %d", len(k.cache))
	}
	// Shared entries may be read concurrently, but their group slices belong to callers.
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := k.Authenticate(context.Background(), "0")
			if err != nil || p.Identity != "0" || !slices.Equal(p.Groups, []string{"team"}) {
				t.Error("cache isolation failed")
				return
			}
			p.Groups[0] = "mutated"
		}()
	}
	wg.Wait()
	clock.Store(1)
	if _, err := k.Authenticate(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	if len(k.cache) != 1 {
		t.Fatal("expired capacity was not reclaimed")
	}
	// Cold concurrent requests must also publish independent, race-free entries.
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token := fmt.Sprintf("new-%d", i)
			p, err := k.Authenticate(context.Background(), token)
			if err != nil || p.Identity != token {
				t.Error("concurrent cache miss mixed identities")
			}
		}()
	}
	wg.Wait()
	if len(k.cache) != 33 {
		t.Fatal("concurrent reviews were not cached")
	}
}

func TestSlowReviewDoesNotExtendCacheLifetime(t *testing.T) {
	var clock atomic.Int64
	var calls atomic.Int32
	endpoint, _ := testCluster(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); clock.Add(2); successReview(w) })
	k, err := New(Config{Audiences: []string{"aqsh"}, CacheTTL: time.Second, TokenReview: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	for i := 0; i < 2; i++ {
		if _, err := k.Authenticate(context.Background(), "caller"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 || len(k.cache) != 0 {
		t.Fatal("slow review extended the authentication lifetime")
	}
}
