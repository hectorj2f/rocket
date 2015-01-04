package cas

import (
	"testing"
	"time"
)

func TestCacheControl(t *testing.T) {
	cc1 := NewCache("max-age=10 no-cache no-store")
	if !cc1.NoStore {
		t.Errorf("expected a no-store header argument for cache-control")
	}
	if !cc1.NoCache {
		t.Errorf("expected a no-cache header argument for cache-control")
	}
	if cc1.MaxAge != 10 {
		t.Errorf("expected max-age header argument for cache-control")
	}

	cc2 := NewCache("max-age=10")
	// Sleep during 11 seconds
	time.Sleep(11000 * time.Millisecond)
	if cc2.UseCachedImage() {
		t.Errorf("expected max-age header argument to be expired after sleep time")
	}
	if cc2.NoStore {
		t.Errorf("unexpected a no-store header argument to be false")
	}
	if cc2.NoCache {
		t.Errorf("unexpected a no-cache header argument to be false")
	}
}
