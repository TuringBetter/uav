package runtime

import (
	"sync"
	"time"
)

// timerEntry holds the state for a single named recurring timer.
type timerEntry struct {
	ticker *time.Ticker
	stopCh chan struct{}
}

// TimerManager manages named, recurring timers for the framework layer.
// Each timer fires OnTick(name) on the registered callback at a set interval.
//
// Improvement over spec: named timers let a single algorithm instance
// distinguish between multiple independent tick sources (e.g., "heartbeat"
// vs "election").
type TimerManager struct {
	mu       sync.Mutex
	timers   map[string]*timerEntry
	callback func(name string) // called on each tick
}

// NewTimerManager creates a TimerManager that calls cb(name) when any named timer fires.
func NewTimerManager(cb func(name string)) *TimerManager {
	return &TimerManager{
		timers:   make(map[string]*timerEntry),
		callback: cb,
	}
}

// Set registers (or resets) the named timer to fire every d.
// If a timer with name already exists it is cancelled first.
func (tm *TimerManager) Set(name string, d time.Duration) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if e, ok := tm.timers[name]; ok {
		close(e.stopCh)
		e.ticker.Stop()
	}

	stopCh := make(chan struct{})
	ticker := time.NewTicker(d)
	tm.timers[name] = &timerEntry{ticker: ticker, stopCh: stopCh}

	go func() {
		for {
			select {
			case <-ticker.C:
				tm.callback(name)
			case <-stopCh:
				ticker.Stop()
				return
			}
		}
	}()
}

// Cancel stops the named timer. No-op if name is unknown.
func (tm *TimerManager) Cancel(name string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if e, ok := tm.timers[name]; ok {
		close(e.stopCh)
		e.ticker.Stop()
		delete(tm.timers, name)
	}
}

// StopAll cancels every active timer.
func (tm *TimerManager) StopAll() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	for name, e := range tm.timers {
		close(e.stopCh)
		e.ticker.Stop()
		delete(tm.timers, name)
	}
}
