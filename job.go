package main

import (
	"context"
	"sync"
	"time"
)

// Per-call deadlines for the two upstream fetchers. Each matches what the
// fetcher itself allows: quotes.Fetcher has its own 45s client timeout, and
// flights.Fetcher makes up to two sequential SerpApi calls (outbound, then
// return) at 45s each, so its budget has to comfortably cover both.
const (
	hotelFetchTimeout  = 45 * time.Second
	flightFetchTimeout = 100 * time.Second
)

// job is the shared throttle behind every background search in this app:
// at most one run at a time, and a new run only once cooldown has elapsed
// since the last one *finished*. All four refreshers below embed it, so a
// visitor leaning on a refresh button cannot turn the site into a scraper
// nor burn the shared SerpApi quota.
type job struct {
	cooldown time.Duration

	mu      sync.Mutex
	running bool
	last    time.Time
}

// start runs fn in the background unless a run is already in flight or the
// cooldown has not elapsed, in which case it reports how long is left.
//
// On success it also returns the server's own clock reading for the start of
// this run — the caller echoes it back to the client, which must compare it
// against Quote.Ts (also server time) instead of its own clock. Comparing a
// browser's Date.now() against a server timestamp breaks under any clock
// drift between the two. fn receives that same reading, for the runs that
// persist it (see miamiRefresher).
func (j *job) start(fn func(startedAt time.Time)) (time.Time, time.Duration, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.running {
		return time.Time{}, j.cooldown, false
	}
	if wait := time.Until(j.last.Add(j.cooldown)); wait > 0 {
		return time.Time{}, wait, false
	}

	startedAt := time.Now()
	j.running = true
	go func() {
		defer j.finish()
		fn(startedAt)
	}()
	return startedAt, 0, true
}

// finish clears the in-flight flag and starts the cooldown. Note that the
// cooldown is measured from when a run *ends*, not when it starts, so a slow
// run doesn't eat into the quiet period that protects the shared quota.
func (j *job) finish() {
	j.mu.Lock()
	j.running = false
	j.last = time.Now()
	j.mu.Unlock()
}

// nextAllowed is when the next run becomes possible, for the client's
// countdown. Zero time before the first run.
func (j *job) nextAllowed() time.Time {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.last.IsZero() {
		return time.Time{}
	}
	return j.last.Add(j.cooldown)
}

// nextAllowedMillis is nextAllowed for the wire: 0 means "no run yet, go
// ahead". Sending the zero time through UnixMilli instead would put a
// timestamp from year 1 on the wire, which a client comparing against its
// own clock reads as a deadline far in the past — right answer by accident,
// but only until someone renders it as a date.
func (j *job) nextAllowedMillis() int64 {
	t := j.nextAllowed()
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// fetchWithin gives one upstream call its own deadline and disposes of the
// cancel, so the refreshers below read as a list of fetches instead of a
// ctx/cancel dance repeated at every call site.
func fetchWithin[T any](d time.Duration, fetch func(context.Context) T) T {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return fetch(ctx)
}
