package cache

import (
	"sync"
	"time"

	"github.com/xuxife/go-generics-cache/policy/clock"
	"github.com/xuxife/go-generics-cache/policy/fifo"
	"github.com/xuxife/go-generics-cache/policy/lfu"
	"github.com/xuxife/go-generics-cache/policy/lru"
	"github.com/xuxife/go-generics-cache/policy/mru"
	"github.com/xuxife/go-generics-cache/policy/simple"
)

// Interface is a common-cache interface.
type Interface[K comparable, V any] interface {
	Get(key K) (value V, ok bool)
	Set(key K, val V)
	Keys() []K
	Delete(key K)
}

var (
	_ = []Interface[struct{}, any]{
		(*simple.Cache[struct{}, any])(nil),
		(*lru.Cache[struct{}, any])(nil),
		(*lfu.Cache[struct{}, any])(nil),
		(*fifo.Cache[struct{}, any])(nil),
		(*mru.Cache[struct{}, any])(nil),
		(*clock.Cache[struct{}, any])(nil),
	}
)

// Item is an item
type Item[K comparable, V any] struct {
	Key        K
	Value      V
	Expiration time.Duration
}

var nowFunc = time.Now

// ItemOption is an option for cache item.
type ItemOption func(*itemOptions)

type itemOptions struct {
	expiration time.Duration // default none
}

// WithExpiration is an option to set expiration time for any items.
// If the expiration is zero or negative value, it treats as w/o expiration.
func WithExpiration(exp time.Duration) ItemOption {
	return func(o *itemOptions) {
		o.expiration = exp
	}
}

// newItem creates a new item with specified any options.
func newItem[K comparable, V any](key K, val V, opts ...ItemOption) *Item[K, V] {
	o := new(itemOptions)
	for _, optFunc := range opts {
		optFunc(o)
	}
	return &Item[K, V]{
		Key:        key,
		Value:      val,
		Expiration: o.expiration,
	}
}

// Cache is a thread safe cache.
type Cache[K comparable, V any] struct {
	cache       Interface[K, *Item[K, V]]
	expirations map[K]chan struct{}
	expireHook  func(K, V)
	// mu is used to do lock in some method process.
	mu sync.Mutex
}

// Option is an option for cache.
type Option[K comparable, V any] func(*options[K, V])

type options[K comparable, V any] struct {
	cache       Interface[K, *Item[K, V]]
	expireHooks []func(K, V)
}

func newOptions[K comparable, V any]() *options[K, V] {
	return &options[K, V]{
		cache: simple.NewCache[K, *Item[K, V]](),
	}
}

// AsLRU is an option to make a new Cache as LRU algorithm.
func AsLRU[K comparable, V any](opts ...lru.Option) Option[K, V] {
	return func(o *options[K, V]) {
		o.cache = lru.NewCache[K, *Item[K, V]](opts...)
	}
}

// AsLFU is an option to make a new Cache as LFU algorithm.
func AsLFU[K comparable, V any](opts ...lfu.Option) Option[K, V] {
	return func(o *options[K, V]) {
		o.cache = lfu.NewCache[K, *Item[K, V]](opts...)
	}
}

// AsFIFO is an option to make a new Cache as FIFO algorithm.
func AsFIFO[K comparable, V any](opts ...fifo.Option) Option[K, V] {
	return func(o *options[K, V]) {
		o.cache = fifo.NewCache[K, *Item[K, V]](opts...)
	}
}

// AsMRU is an option to make a new Cache as MRU algorithm.
func AsMRU[K comparable, V any](opts ...mru.Option) Option[K, V] {
	return func(o *options[K, V]) {
		o.cache = mru.NewCache[K, *Item[K, V]](opts...)
	}
}

// AsClock is an option to make a new Cache as clock algorithm.
func AsClock[K comparable, V any](opts ...clock.Option) Option[K, V] {
	return func(o *options[K, V]) {
		o.cache = clock.NewCache[K, *Item[K, V]](opts...)
	}
}

// WithExpireHook registers a new hook function that will be called when item is expired.
func WithExpireHook[K comparable, V any](fn func(K, V)) Option[K, V] {
	return func(o *options[K, V]) {
		o.expireHooks = append(o.expireHooks, fn)
	}
}

// New creates a new thread safe Cache.
//
// There are several Cache replacement policies available with you specified any options.
func New[K comparable, V any](opts ...Option[K, V]) *Cache[K, V] {
	o := newOptions[K, V]()
	for _, optFunc := range opts {
		optFunc(o)
	}
	return &Cache[K, V]{
		cache:       o.cache,
		expirations: make(map[K]chan struct{}, 0),
		expireHook: func(k K, v V) {
			for _, fn := range o.expireHooks {
				fn(k, v)
			}
		},
	}
}

// Get looks up a key's value from the cache.
func (c *Cache[K, V]) Get(key K) (value V, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.cache.Get(key)
	if !ok {
		return
	}
	return item.Value, true
}

// Set sets a value to the cache with key. replacing any existing value.
func (c *Cache[K, V]) Set(key K, val V, opts ...ItemOption) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item := newItem(key, val, opts...)
	if item.Expiration <= 0 {
		c.cache.Set(key, item)
		return
	}

	if _, ok := c.cache.Get(key); ok {
		c.doneWatchExpiration(key)
	}

	c.cache.Set(key, item)
	c.installExpirationWatcher(item.Key, item.Expiration)
}

func (c *Cache[K, V]) installExpirationWatcher(key K, exp time.Duration) {
	done := make(chan struct{})
	c.expirations[key] = done
	go func() {
		select {
		case <-time.After(exp):
			value, ok := c.cache.Get(key)
			if ok {
				go c.expireHook(value.Key, value.Value)
			}
			c.Delete(key)
		case <-done:
		}
	}()
}

func (c *Cache[K, V]) doneWatchExpiration(key K) {
	if ch, ok := c.expirations[key]; ok {
		close(ch)
	}
}

// Keys returns the keys of the cache. the order is relied on algorithms.
func (c *Cache[K, V]) Keys() []K {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cache.Keys()
}

// Delete deletes the item with provided key from the cache.
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.Delete(key)
}

// Contains reports whether key is within cache.
func (c *Cache[K, V]) Contains(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.cache.Get(key)
	return ok
}

// NumberCache is a in-memory cache which is able to store only Number constraint.
type NumberCache[K comparable, V Number] struct {
	*Cache[K, V]
	// nmu is used to do lock in Increment/Decrement process.
	// Note that this must be here as a separate mutex because mu in Cache struct is Locked in Get,
	// and if we call mu.Lock in Increment/Decrement, it will cause deadlock.
	nmu sync.Mutex
}

// NewNumber creates a new cache for Number constraint.
func NewNumber[K comparable, V Number](opts ...Option[K, V]) *NumberCache[K, V] {
	return &NumberCache[K, V]{
		Cache: New(opts...),
	}
}

// Increment an item of type Number constraint by n.
// Returns the incremented value.
func (nc *NumberCache[K, V]) Increment(key K, n V) V {
	// In order to avoid lost update, we must lock whole Increment/Decrement process.
	nc.nmu.Lock()
	defer nc.nmu.Unlock()
	got, _ := nc.Cache.Get(key)
	nv := got + n
	nc.Cache.Set(key, nv)
	return nv
}

// Decrement an item of type Number constraint by n.
// Returns the decremented value.
func (nc *NumberCache[K, V]) Decrement(key K, n V) V {
	nc.nmu.Lock()
	defer nc.nmu.Unlock()
	got, _ := nc.Cache.Get(key)
	nv := got - n
	nc.Cache.Set(key, nv)
	return nv
}
