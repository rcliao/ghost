package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// CachedEmbedder wraps an Embedder with a file-backed cache.
// Cache hits skip the underlying embedder entirely.
// Useful for benchmarks where the same texts are embedded repeatedly.
type CachedEmbedder struct {
	inner    Embedder
	path     string // file path for cache persistence
	mu       sync.RWMutex
	cache    map[string]Vector // content hash → vector
	dirty    bool
}

// NewCachedEmbedder wraps an embedder with a file-backed cache.
// If the cache file exists, it is loaded. Call Save() to persist new entries.
func NewCachedEmbedder(inner Embedder, cachePath string) *CachedEmbedder {
	c := &CachedEmbedder{
		inner: inner,
		path:  cachePath,
		cache: make(map[string]Vector),
	}
	// Load existing cache if present
	if data, err := os.ReadFile(cachePath); err == nil {
		json.Unmarshal(data, &c.cache) // ignore errors — start fresh on corrupt cache
	}
	return c
}

// ContentHash returns a hex-encoded hash of the text, used as cache key.
func ContentHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:16]) // 128-bit prefix is sufficient
}

func (c *CachedEmbedder) Embed(ctx context.Context, text string) (Vector, error) {
	key := ContentHash(text)

	c.mu.RLock()
	if vec, ok := c.cache[key]; ok {
		c.mu.RUnlock()
		return vec, nil
	}
	c.mu.RUnlock()

	vec, err := c.inner.Embed(ctx, text)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[key] = vec
	c.dirty = true
	c.mu.Unlock()

	return vec, nil
}

func (c *CachedEmbedder) EmbedBatch(ctx context.Context, texts []string) ([]Vector, error) {
	vecs := make([]Vector, len(texts))
	var uncached []int // indices of texts not in cache

	c.mu.RLock()
	for i, t := range texts {
		key := ContentHash(t)
		if vec, ok := c.cache[key]; ok {
			vecs[i] = vec
		} else {
			uncached = append(uncached, i)
		}
	}
	c.mu.RUnlock()

	if len(uncached) == 0 {
		return vecs, nil
	}

	// Batch embed uncached texts
	uncachedTexts := make([]string, len(uncached))
	for i, idx := range uncached {
		uncachedTexts[i] = texts[idx]
	}

	newVecs, err := EmbedBatch(ctx, c.inner, uncachedTexts)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	for i, idx := range uncached {
		vecs[idx] = newVecs[i]
		c.cache[ContentHash(texts[idx])] = newVecs[i]
	}
	c.dirty = true
	c.mu.Unlock()

	return vecs, nil
}

func (c *CachedEmbedder) Dims() int { return c.inner.Dims() }

// Len returns the number of cached embeddings.
func (c *CachedEmbedder) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache)
}

// Save persists the cache to disk.
func (c *CachedEmbedder) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.dirty {
		return nil
	}
	data, err := json.Marshal(c.cache)
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	if err := os.WriteFile(c.path, data, 0o644); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	c.dirty = false
	return nil
}
