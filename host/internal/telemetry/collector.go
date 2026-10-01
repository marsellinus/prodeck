// Package telemetry samples host metrics and fans them out to subscribed
// clients.
//
// One sampling loop serves every subscriber, deliberately: a CPU percentage is
// a delta between two readings, so concurrent independent samplers would each
// see a fraction of the interval and report nonsense. The loop samples at the
// fastest rate anyone asked for, and each subscriber is served on its own
// cadence.
package telemetry

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/platform"
)

// Bounds on the sampling interval. The floor keeps a client from asking the
// host to poll /proc every millisecond; the ceiling keeps an idle deck from
// going silent for minutes.
const (
	MinInterval = 250 * time.Millisecond
	MaxInterval = 60 * time.Second
	// idleInterval is how often the loop wakes when nobody is subscribed.
	idleInterval = 5 * time.Second
	// sampleTimeout bounds one sampling pass. A hung platform call must not
	// wedge the loop.
	sampleTimeout = 2 * time.Second
)

// Subscription is one client's view of the metric stream.
type Subscription struct {
	ID       string
	Metrics  []string
	Interval time.Duration

	ch   chan Sample
	once sync.Once
	done chan struct{}
}

// C is the channel of samples. It is closed when the subscription ends.
func (s *Subscription) C() <-chan Sample { return s.ch }

// Stop ends the subscription. It is idempotent.
func (s *Subscription) Stop() {
	s.once.Do(func() { close(s.done) })
}

// Sample is one set of readings.
type Sample struct {
	TS     int64              `json:"ts"`
	Values map[string]float64 `json:"values"`
}

// Collector owns the sampling loop.
type Collector struct {
	plat *platform.Platform
	log  *slog.Logger

	mu      sync.Mutex
	subs    map[string]*Subscription
	latest  map[string]float64
	lastErr error

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	started  bool
}

// New creates a collector. Call Start to begin sampling.
func New(plat *platform.Platform, log *slog.Logger) *Collector {
	if log == nil {
		log = slog.Default()
	}
	return &Collector{
		plat:   plat,
		log:    log,
		subs:   make(map[string]*Subscription),
		latest: make(map[string]float64),
		stop:   make(chan struct{}),
	}
}

// Start begins the sampling loop.
func (c *Collector) Start() {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.mu.Unlock()

	c.wg.Add(1)
	go c.loop()
}

// Close stops the loop and ends every subscription.
func (c *Collector) Close() {
	c.stopOnce.Do(func() { close(c.stop) })
	c.wg.Wait()

	c.mu.Lock()
	subs := make([]*Subscription, 0, len(c.subs))
	for _, s := range c.subs {
		subs = append(subs, s)
	}
	c.subs = make(map[string]*Subscription)
	c.mu.Unlock()
	for _, s := range subs {
		s.Stop()
	}
}

// Available lists the metrics this host can produce.
func (c *Collector) Available() []string { return c.plat.Metrics.Available() }

// Latest returns the most recent readings, for a client that has just connected
// or subscribed and should not wait a full interval for its first value.
func (c *Collector) Latest(metrics []string) Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Sample{TS: time.Now().UnixMilli(), Values: filter(c.latest, metrics)}
}

// Subscribe registers a client. An empty metrics list means "everything this
// host produces". The interval is clamped, never rejected: a client asking for
// too fast a rate gets the fastest the host will give, which is more useful
// than an error.
func (c *Collector) Subscribe(id string, metrics []string, interval time.Duration) *Subscription {
	if len(metrics) == 0 {
		metrics = c.Available()
	}
	interval = clampInterval(interval)

	sub := &Subscription{
		ID:       id,
		Metrics:  metrics,
		Interval: interval,
		ch:       make(chan Sample, 4),
		done:     make(chan struct{}),
	}

	c.mu.Lock()
	if old, ok := c.subs[id]; ok {
		// Re-subscribing replaces the previous subscription rather than
		// stacking a second one, which is what a client that changes its
		// metric list expects.
		old.Stop()
	}
	c.subs[id] = sub
	latest := filter(c.latest, metrics)
	c.mu.Unlock()

	if len(latest) > 0 {
		select {
		case sub.ch <- Sample{TS: time.Now().UnixMilli(), Values: latest}:
		default:
		}
	}
	return sub
}

// Unsubscribe removes a subscription by id.
func (c *Collector) Unsubscribe(id string) {
	c.mu.Lock()
	sub, ok := c.subs[id]
	if ok {
		delete(c.subs, id)
	}
	c.mu.Unlock()
	if ok {
		sub.Stop()
	}
}

// SubscriberCount reports how many clients are subscribed.
func (c *Collector) SubscriberCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subs)
}

func (c *Collector) loop() {
	defer c.wg.Done()

	timer := time.NewTimer(MinInterval)
	defer timer.Stop()

	for {
		select {
		case <-c.stop:
			return
		case <-timer.C:
		}

		subs, interval := c.snapshot()
		if len(subs) == 0 {
			// Nobody is watching: do not sample. Sampling an idle host for no
			// consumer is pure waste, and on Linux it means reading /proc
			// forever for nothing.
			timer.Reset(idleInterval)
			continue
		}

		c.sampleOnce()

		c.mu.Lock()
		latest := c.latest
		c.mu.Unlock()

		now := time.Now()
		for _, s := range subs {
			select {
			case <-s.done:
				continue
			default:
			}
			values := filter(latest, s.Metrics)
			if len(values) == 0 {
				continue
			}
			sample := Sample{TS: now.UnixMilli(), Values: values}
			// A full buffer means the client is not draining; drop the oldest
			// so it gets fresh data rather than a backlog. Telemetry is a
			// stream of current values, not a queue of history.
			select {
			case s.ch <- sample:
			default:
				select {
				case <-s.ch:
				default:
				}
				select {
				case s.ch <- sample:
				default:
				}
			}
		}

		timer.Reset(interval)
	}
}

// snapshot returns the current subscriptions and the interval the loop should
// tick at.
func (c *Collector) snapshot() ([]*Subscription, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Subscription, 0, len(c.subs))
	interval := MaxInterval
	for _, s := range c.subs {
		select {
		case <-s.done:
			delete(c.subs, s.ID)
			continue
		default:
		}
		out = append(out, s)
		if s.Interval < interval {
			interval = s.Interval
		}
	}
	if len(out) == 0 {
		return nil, MaxInterval
	}
	return out, clampInterval(interval)
}

func (c *Collector) sampleOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), sampleTimeout)
	defer cancel()

	values, err := c.plat.Metrics.Sample(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		// Keep the previous values: a single failed read is not a reason to
		// blank every client's gauges. The error is reported once per change.
		if c.lastErr == nil || c.lastErr.Error() != err.Error() {
			c.log.Warn("telemetry sampling failed", "error", err)
		}
		c.lastErr = err
		return
	}
	c.lastErr = nil
	for k, v := range values {
		c.latest[k] = v
	}
}

func clampInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	if d < MinInterval {
		return MinInterval
	}
	if d > MaxInterval {
		return MaxInterval
	}
	return d
}

// filter selects the requested metrics, keeping only those that exist.
func filter(values map[string]float64, metrics []string) map[string]float64 {
	if len(metrics) == 0 {
		out := make(map[string]float64, len(values))
		for k, v := range values {
			out[k] = v
		}
		return out
	}
	out := make(map[string]float64, len(metrics))
	for _, m := range metrics {
		if v, ok := values[m]; ok {
			out[m] = v
		}
	}
	return out
}
