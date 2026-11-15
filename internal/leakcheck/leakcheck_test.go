// SPDX-License-Identifier: MIT

package leakcheck

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// recorder is a TB that keeps what Check reports.
type recorder struct {
	cleanups []func()
	errors   []string
}

func (r *recorder) Helper()          {}
func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// finish runs the cleanups, last first, as the testing package does.
func (r *recorder) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
	r.cleanups = nil
}

// leftBehind blocks until release is closed. Its name is what the report of
// a goroutine that is still running must show.
func leftBehind(started chan<- struct{}, release <-chan struct{}) {
	close(started)
	<-release
}

// A goroutine that outlives the test is reported with its stack, which
// names the function it runs; a goroutine that ended, or that was running
// before Check, is not.
func TestCheckNamesTheGoroutine(t *testing.T) {
	Check(t) // of this test itself: release ends everything below

	earlier, release := make(chan struct{}), make(chan struct{})
	go leftBehind(earlier, release) // running before the check: not its business
	<-earlier

	r := &recorder{}
	check(r, 50*time.Millisecond)
	started := make(chan struct{})
	go leftBehind(started, release)
	<-started
	ended := make(chan struct{})
	go func() { close(ended) }() // started during the test, and over
	<-ended
	r.finish()
	if len(r.errors) != 1 {
		t.Fatalf("reported %d times, want once: %q", len(r.errors), r.errors)
	}
	msg := r.errors[0]
	if !strings.HasPrefix(msg, "1 goroutines started during the test are still running") {
		t.Errorf("the report does not count one goroutine:\n%s", msg)
	}
	if !strings.Contains(msg, "leakcheck.leftBehind(") || !strings.Contains(msg, "goroutine ") {
		t.Errorf("the report does not name the goroutine and its function:\n%s", msg)
	}
	if n := strings.Count(msg, "leakcheck.leftBehind("); n != 1 {
		t.Errorf("the report shows leftBehind %d times: the goroutine that ran before the check is in it:\n%s", n, msg)
	}

	// Once it has returned there is nothing to report, also when it takes a
	// moment to end: the check waits for it.
	r = &recorder{}
	check(r, Wait)
	slow := make(chan struct{})
	go func() { <-slow }()
	time.AfterFunc(20*time.Millisecond, func() { close(slow) })
	close(release)
	r.finish()
	if len(r.errors) != 0 {
		t.Fatalf("goroutines that ended were reported: %q", r.errors)
	}
}

// Goroutines that end while others start do not hide them: the count of
// goroutines is the same before and after, and the new one is reported.
func TestCheckDoesNotCount(t *testing.T) {
	Check(t)
	old, release := make(chan struct{}), make(chan struct{})
	stop := make(chan struct{})
	go leftBehind(old, stop)
	<-old

	r := &recorder{}
	check(r, 50*time.Millisecond)
	close(stop) // one goroutine from before ends
	started := make(chan struct{})
	go leftBehind(started, release) // and one starts
	<-started
	r.finish()
	close(release)
	if len(r.errors) != 1 || !strings.Contains(r.errors[0], "leakcheck.leftBehind(") {
		t.Fatalf("the goroutine that started was not reported: %q", r.errors)
	}
}
