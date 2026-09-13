package par

import (
	"runtime"
	"sync/atomic"
	"testing"
)

// Every index has to be handled exactly once, or a caller writing results into a
// slice it owns would silently lose or duplicate work.
func TestDoVisitsEveryIndexExactlyOnce(t *testing.T) {
	for _, n := range []int{0, 1, 2, 7, 64, 1000} {
		counts := make([]int32, n)
		Do(n, func(i int) { atomic.AddInt32(&counts[i], 1) })
		for i, c := range counts {
			if c != 1 {
				t.Fatalf("n=%d: index %d handled %d times, want 1", n, i, c)
			}
		}
	}
}

// Do returns only once every goroutine has finished, which is what lets the
// caller read the slice it passed in without any synchronisation of its own.
func TestDoWaitsForEveryWorker(t *testing.T) {
	const n = 256
	out := make([]int, n)
	Do(n, func(i int) { out[i] = i * i })
	for i := range out {
		if out[i] != i*i {
			t.Fatalf("out[%d] = %d, want %d: Do returned before its workers did", i, out[i], i*i)
		}
	}
}

// A single index must not pay for a goroutine, and must not deadlock either.
func TestDoRunsInlineBelowTwoItems(t *testing.T) {
	before := runtime.NumGoroutine()
	called := 0
	Do(1, func(int) { called++ })
	if called != 1 {
		t.Fatalf("body called %d times, want 1", called)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines went %d -> %d; a single item should run inline", before, after)
	}
}

// n <= 0 is reached whenever a project has nothing of some kind to load.
func TestDoOnNothingIsANoOp(t *testing.T) {
	for _, n := range []int{0, -1} {
		called := false
		Do(n, func(int) { called = true })
		if called {
			t.Errorf("n=%d: body ran", n)
		}
	}
}

// GOMAXPROCS=1 takes the inline path for every n, so the body still has to see
// every index.
func TestDoIsCorrectOnASingleProcessor(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	const n = 32
	seen := make([]bool, n)
	Do(n, func(i int) { seen[i] = true })
	for i, ok := range seen {
		if !ok {
			t.Fatalf("index %d never handled with GOMAXPROCS=1", i)
		}
	}
}

// The work per index is deliberately uneven: a fixed split would leave one
// worker with the whole tail, so the claim counter has to hand indices out.
func TestDoBalancesUnevenWork(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("needs more than one processor to say anything")
	}
	const n = 512
	var total atomic.Int64
	Do(n, func(i int) {
		work := 1
		if i > n-8 {
			work = 1000
		}
		sum := 0
		for j := 0; j < work; j++ {
			sum += j
		}
		total.Add(int64(sum % 7))
	})
	// The assertion that matters is that this returned at all, and that nothing
	// raced: `go test -race` is what makes this test worth running.
	_ = total.Load()
}

func BenchmarkDo(b *testing.B) {
	out := make([]int, 1024)
	for b.Loop() {
		Do(len(out), func(i int) { out[i] = i })
	}
}
