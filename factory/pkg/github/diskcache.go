package github

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// EnvCacheDir names a directory to keep the GitHub response cache in, so
// that it outlives the process: the overseer restarts `factory watch`
// every few minutes, and each factory subprocess is a process of its own,
// so a cache in memory alone starts cold every time. Unset, the cache is
// in memory only.
const EnvCacheDir = "FACTORY_GITHUB_CACHE_DIR"

// EnvCacheMaxBytes caps one token's directory, in bytes; unset or not a
// positive number, defaultDiskCacheBytes.
const EnvCacheMaxBytes = "FACTORY_GITHUB_CACHE_MAX_BYTES"

const defaultDiskCacheBytes = 200 << 20

// diskCache is an httpcache.Cache keeping one token's responses in a
// directory of its own, a file per URL, bounded in bytes. Processes may
// share the directory: a write goes to a temporary file renamed into
// place, so a reader sees a whole response or none, and anything it
// cannot read is a miss. The files can hold a private repository's
// content, so the directory is 0700 and the files 0600.
type diskCache struct {
	dir      string
	maxBytes int64

	mu   sync.Mutex
	size int64 // this process's estimate of the directory's size
}

func newDiskCache(dir string, maxBytes int64) (*diskCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := &diskCache{dir: dir, maxBytes: maxBytes}
	c.size = c.measure()
	return c, nil
}

func (c *diskCache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:]))
}

func (c *diskCache) Get(key string) ([]byte, bool) {
	p := c.path(key)
	data, err := os.ReadFile(p)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	// Its time is when it was last used, for eviction.
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return data, true
}

func (c *diskCache) Set(key string, value []byte) {
	p := c.path(key)
	tmp, err := os.CreateTemp(c.dir, ".tmp-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(value)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	var old int64
	if info, err := os.Stat(p); err == nil {
		old = info.Size()
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	c.mu.Lock()
	c.size += int64(len(value)) - old
	over := c.size > c.maxBytes
	c.mu.Unlock()
	if over {
		c.evict()
	}
}

func (c *diskCache) Delete(key string) {
	p := c.path(key)
	info, err := os.Stat(p)
	if err != nil {
		return
	}
	if os.Remove(p) == nil {
		c.mu.Lock()
		c.size -= info.Size()
		c.mu.Unlock()
	}
}

// evict drops the least recently used files until the directory is under
// nine tenths of its cap, from what is on disk now (another process may
// have written to it too).
func (c *diskCache) evict() {
	c.mu.Lock()
	defer c.mu.Unlock()
	type file struct {
		path string
		size int64
		used time.Time
	}
	var files []file
	var total int64
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{filepath.Join(c.dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].used.Before(files[j].used) })
	target := c.maxBytes / 10 * 9
	for _, f := range files {
		if total <= target {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
	c.size = total
}

func (c *diskCache) measure() int64 {
	var total int64
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			total += info.Size()
		}
	}
	return total
}

// diskCacheBytes is the cap EnvCacheMaxBytes sets.
func diskCacheBytes() int64 {
	if n, err := strconv.ParseInt(os.Getenv(EnvCacheMaxBytes), 10, 64); err == nil && n > 0 {
		return n
	}
	return defaultDiskCacheBytes
}

// tieredCache reads memory first, then disk, and writes both: the
// process's own repeats stay in memory, and a new process starts from
// what the last one left on disk.
type tieredCache struct {
	mem  *lruCache
	disk *diskCache
}

func (c tieredCache) Get(key string) ([]byte, bool) {
	if v, ok := c.mem.Get(key); ok {
		return v, true
	}
	v, ok := c.disk.Get(key)
	if ok {
		c.mem.Set(key, v)
	}
	return v, ok
}

func (c tieredCache) Set(key string, value []byte) {
	c.mem.Set(key, value)
	c.disk.Set(key, value)
}

func (c tieredCache) Delete(key string) {
	c.mem.Delete(key)
	c.disk.Delete(key)
}
