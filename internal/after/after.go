// SPDX-License-Identifier: MIT

// Package after carries the functions a handler registers with
// ocpp.AfterResponse from the handler's context to the engine, which runs
// them once the handler's result is queued.
package after

import (
	"context"
	"sync"
)

type key struct{}

// List collects functions; it is safe for concurrent use.
type List struct {
	mu   sync.Mutex
	done bool
	fs   []func()
}

// With returns ctx carrying a new List.
func With(ctx context.Context) (context.Context, *List) {
	l := &List{}
	return context.WithValue(ctx, key{}, l), l
}

// Add appends f to the List of ctx. It reports false when ctx has none, or
// when the List has already been taken.
func Add(ctx context.Context, f func()) bool {
	l, _ := ctx.Value(key{}).(*List)
	if l == nil || f == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return false
	}
	l.fs = append(l.fs, f)
	return true
}

// Take returns the functions added so far, and refuses later additions.
func (l *List) Take() []func() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = true
	fs := l.fs
	l.fs = nil
	return fs
}
