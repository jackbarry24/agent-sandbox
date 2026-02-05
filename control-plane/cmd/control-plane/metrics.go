package main

import (
	"expvar"
	"sync/atomic"
)

var (
	metricCreates         = expvar.NewInt("sandbox_create_total")
	metricCreateWarmHit   = expvar.NewInt("sandbox_create_warm_hit_total")
	metricCreateCold      = expvar.NewInt("sandbox_create_cold_total")
	metricExecs           = expvar.NewInt("sandbox_exec_total")
	metricDeletes         = expvar.NewInt("sandbox_delete_total")
	metricWarmPoolDesired = expvar.NewInt("warm_pool_desired")
	metricWarmPoolReady   = expvar.NewInt("warm_pool_ready")
	metricCacheMode       = expvar.NewString("sandbox_cache_mode")
	metricStreamBuffer    = expvar.NewInt("sandbox_stream_buffer")
	createReadyTotalMs    int64
	createReadyCount      int64
	createReadyLastMs     int64
)

func init() {
	expvar.Publish("sandbox_create_ready_ms_avg", expvar.Func(func() any {
		cnt := atomic.LoadInt64(&createReadyCount)
		if cnt == 0 {
			return 0
		}
		total := atomic.LoadInt64(&createReadyTotalMs)
		return total / cnt
	}))
	expvar.Publish("sandbox_create_ready_ms_last", expvar.Func(func() any {
		return atomic.LoadInt64(&createReadyLastMs)
	}))
}

func recordCreateReady(durationMs int64) {
	atomic.AddInt64(&createReadyTotalMs, durationMs)
	atomic.AddInt64(&createReadyCount, 1)
	atomic.StoreInt64(&createReadyLastMs, durationMs)
}
