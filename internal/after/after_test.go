// SPDX-License-Identifier: MIT

package after

import (
	"context"
	"testing"
)

func TestList(t *testing.T) {
	if Add(context.Background(), func() {}) {
		t.Fatal("added to a context without a List")
	}
	ctx, l := With(context.Background())
	if Add(ctx, nil) {
		t.Fatal("added nil")
	}
	for range 2 {
		if !Add(ctx, func() {}) {
			t.Fatal("refused")
		}
	}
	if fs := l.Take(); len(fs) != 2 {
		t.Fatalf("took %d", len(fs))
	}
	if Add(ctx, func() {}) {
		t.Fatal("added after Take")
	}
	if fs := l.Take(); len(fs) != 0 {
		t.Fatalf("took %d again", len(fs))
	}
}
