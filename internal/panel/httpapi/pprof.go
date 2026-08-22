package httpapi

import (
	"net/http"
	"net/http/pprof"
)

// registerPprofHandlers adds pprof endpoints for performance profiling.
// These endpoints should only be enabled in development or bound to localhost in production.
//
// Available endpoints:
//   GET /debug/pprof/          - Index page with profile links
//   GET /debug/pprof/cmdline   - Command line that started this process
//   GET /debug/pprof/profile   - CPU profile (30 second default)
//   GET /debug/pprof/symbol    - Symbol lookup
//   GET /debug/pprof/trace     - Execution trace (5 second default)
//   GET /debug/pprof/heap      - Heap profile (memory allocations)
//   GET /debug/pprof/goroutine - Goroutine dump
//   GET /debug/pprof/threadcreate - Thread creation profile
//   GET /debug/pprof/block     - Block profile (contention)
//   GET /debug/pprof/mutex     - Mutex profile (lock contention)
//   GET /debug/pprof/allocs    - All memory allocations (since start)
//
// Usage:
//   # Interactive heap analysis
//   go tool pprof http://localhost:8080/debug/pprof/heap
//
//   # CPU profile for 30 seconds
//   go tool pprof http://localhost:8080/debug/pprof/profile?seconds=30
//
//   # Save heap snapshot
//   curl http://localhost:8080/debug/pprof/heap > heap.prof
//   go tool pprof heap.prof
//
//   # Goroutine dump (text format)
//   curl http://localhost:8080/debug/pprof/goroutine?debug=1
//
//   # Compare two heap snapshots
//   go tool pprof -base heap1.prof heap2.prof
func registerPprofHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}
