package controlapi

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type metricKey struct {
	method, route, statusClass string
}

type requestMetric struct {
	count           uint64
	durationSeconds float64
}

// operationalMetrics intentionally uses only bounded route patterns and HTTP
// status classes. It never labels metrics with tenant, principal, path value,
// token, address, digest, or other attacker-controlled cardinality.
type operationalMetrics struct {
	inFlight atomic.Int64
	mu       sync.Mutex
	requests map[metricKey]requestMetric
}

func newOperationalMetrics() *operationalMetrics {
	return &operationalMetrics{requests: make(map[metricKey]requestMetric)}
}

func (m *operationalMetrics) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.inFlight.Add(1)
		defer m.inFlight.Add(-1)
		started := time.Now()
		response := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(response, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		key := metricKey{method: r.Method, route: route, statusClass: strconv.Itoa(response.status/100) + "xx"}
		m.mu.Lock()
		metric := m.requests[key]
		metric.count++
		metric.durationSeconds += time.Since(started).Seconds()
		m.requests[key] = metric
		m.mu.Unlock()
	})
}

type statusResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (m *operationalMetrics) serve(w http.ResponseWriter, statusProvider func() string, chainID uint64, authorizationsPaused bool) {
	m.mu.Lock()
	keys := make([]metricKey, 0, len(m.requests))
	for key := range m.requests {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].statusClass < keys[j].statusClass
	})
	snapshot := make(map[metricKey]requestMetric, len(m.requests))
	for key, metric := range m.requests {
		snapshot[key] = metric
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintln(w, "# HELP flowops_http_requests_in_flight Current control-plane HTTP requests.")
	fmt.Fprintln(w, "# TYPE flowops_http_requests_in_flight gauge")
	fmt.Fprintf(w, "flowops_http_requests_in_flight %d\n", m.inFlight.Load())
	fmt.Fprintln(w, "# HELP flowops_http_requests_total Completed control-plane HTTP requests by bounded route and status class.")
	fmt.Fprintln(w, "# TYPE flowops_http_requests_total counter")
	for _, key := range keys {
		metric := snapshot[key]
		labels := fmt.Sprintf(`method=%q,route=%q,status_class=%q`, key.method, key.route, key.statusClass)
		fmt.Fprintf(w, "flowops_http_requests_total{%s} %d\n", labels, metric.count)
		fmt.Fprintf(w, "flowops_http_request_duration_seconds_sum{%s} %g\n", labels, metric.durationSeconds)
		fmt.Fprintf(w, "flowops_http_request_duration_seconds_count{%s} %d\n", labels, metric.count)
	}
	fmt.Fprintln(w, "# HELP flowops_chain_state Current fail-closed Base chain state.")
	fmt.Fprintln(w, "# TYPE flowops_chain_state gauge")
	fmt.Fprintf(w, "flowops_chain_state{chain_id=%q,state=%q} 1\n", strconv.FormatUint(chainID, 10), statusProvider())
	fmt.Fprintln(w, "# HELP flowops_authorizations_paused Whether the chain gate currently pauses new authorizations.")
	fmt.Fprintln(w, "# TYPE flowops_authorizations_paused gauge")
	if authorizationsPaused {
		fmt.Fprintln(w, "flowops_authorizations_paused 1")
	} else {
		fmt.Fprintln(w, "flowops_authorizations_paused 0")
	}
}
