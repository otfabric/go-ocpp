// SPDX-License-Identifier: MIT

// Package leakcheck checks that a test leaves no goroutine behind. It
// compares goroutine stacks, not counts: a goroutine that started during
// the test and still runs after it is reported with its stack, whatever
// other goroutines ended meanwhile. It is imported by tests only, and is
// the one such helper of the module (internal/engine, internal/transport
// and e2e use it).
package leakcheck

import (
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Wait is how long Check waits for goroutines that are ending.
const Wait = 10 * time.Second

// TB is what Check needs of a *testing.T.
type TB interface {
	Helper()
	Cleanup(func())
	Errorf(format string, args ...any)
}

// Check fails t, when the test and the cleanups registered after this call
// are over, for every goroutine that did not exist when Check was called and
// has not returned within Wait. Call it first in a test, so that its cleanup
// runs last.
func Check(t TB) {
	t.Helper()
	check(t, Wait)
}

func check(t TB, wait time.Duration) {
	t.Helper()
	before := map[uint64]bool{}
	for id := range goroutines() {
		before[id] = true
	}
	t.Cleanup(func() {
		t.Helper()
		deadline := time.Now().Add(wait)
		for {
			left := started(before)
			if len(left) == 0 {
				return
			}
			if !time.Now().Before(deadline) {
				t.Errorf("%d goroutines started during the test are still running after %v:\n\n%s", len(left), wait, strings.Join(left, "\n\n"))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// started returns the stacks of the goroutines that are not in before, in
// the order the runtime lists them.
func started(before map[uint64]bool) []string {
	var left []string
	for id, stack := range goroutines() {
		if !before[id] {
			left = append(left, stack)
		}
	}
	return left
}

// goroutines yields the ID and the stack of every goroutine but the calling
// one, as runtime.Stack prints them: "goroutine 7 [chan receive]:" and the
// frames below it.
func goroutines() func(yield func(uint64, string) bool) {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	return func(yield func(uint64, string) bool) {
		// The first block is the calling goroutine.
		for i, stack := range strings.Split(strings.TrimSpace(string(buf)), "\n\n") {
			if i == 0 {
				continue
			}
			rest, ok := strings.CutPrefix(stack, "goroutine ")
			if !ok {
				continue
			}
			digits, _, _ := strings.Cut(rest, " ")
			id, err := strconv.ParseUint(digits, 10, 64)
			if err != nil {
				continue
			}
			if !yield(id, stack) {
				return
			}
		}
	}
}
