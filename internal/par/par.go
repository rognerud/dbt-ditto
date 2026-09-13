// Package par runs independent work across the CPUs, which is how this tool
// loads projects, reads schema files, resolves nodes and asks providers.
package par

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// Do runs body for every index in [0, n), one goroutine per CPU. It returns once
// every index has been handled, so results may be written into a slice the caller
// owns, one index per goroutine.
func Do(n int, body func(i int)) {
	workers := min(runtime.GOMAXPROCS(0), n)
	if workers <= 1 {
		for i := range n {
			body(i)
		}
		return
	}

	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := int(next.Add(1)) - 1; i < n; i = int(next.Add(1)) - 1 {
				body(i)
			}
		}()
	}
	wg.Wait()
}
