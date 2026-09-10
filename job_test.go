package main

import (
	"testing"
	"time"
)

func TestJobStartRunsOnce(t *testing.T) {
	j := &job{}
	release := make(chan struct{})
	started := make(chan time.Time, 1)

	at, _, ok := j.start(func(startedAt time.Time) {
		started <- startedAt
		<-release
	})
	if !ok {
		t.Fatal("first start was refused")
	}
	if at.IsZero() {
		t.Error("start returned a zero startedAt")
	}

	got := <-started
	if !got.Equal(at) {
		t.Errorf("fn saw startedAt %v, caller got %v", got, at)
	}

	// A second start while the first is still in flight must be refused,
	// even with no cooldown configured.
	if _, _, ok := j.start(func(time.Time) { t.Error("second run should not have started") }); ok {
		t.Error("start was allowed while a run was in flight")
	}

	close(release)
	waitIdle(t, j)
}

func TestJobCooldownBlocksNextRun(t *testing.T) {
	j := &job{cooldown: time.Hour}

	if _, _, ok := j.start(func(time.Time) {}); !ok {
		t.Fatal("first start was refused")
	}
	waitIdle(t, j)

	_, wait, ok := j.start(func(time.Time) { t.Error("run started during cooldown") })
	if ok {
		t.Fatal("start was allowed during the cooldown")
	}
	if wait <= 0 || wait > time.Hour {
		t.Errorf("wait = %v, want something inside (0, 1h]", wait)
	}

	// nextAllowed is what the client counts down to, so it has to sit
	// roughly one cooldown ahead — never in the past.
	if until := time.Until(j.nextAllowed()); until <= 0 {
		t.Errorf("nextAllowed is %v away, want a future time", until)
	}
}

func TestJobAllowsRunAfterCooldownElapses(t *testing.T) {
	j := &job{cooldown: time.Millisecond}

	if _, _, ok := j.start(func(time.Time) {}); !ok {
		t.Fatal("first start was refused")
	}
	waitIdle(t, j)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, ok := j.start(func(time.Time) {}); ok {
			waitIdle(t, j)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cooldown of 1ms never elapsed")
}

// waitIdle blocks until the job's in-flight run has released it, so a test
// can look at last/running without racing the goroutine start() spawned.
func waitIdle(t *testing.T, j *job) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		j.mu.Lock()
		running := j.running
		j.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job still running after 2s")
}
