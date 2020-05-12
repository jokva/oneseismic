package profiling

import (
	"net/http"
	"net/http/pprof"

	l "github.com/equinor/oneseismic/api/logger"
	"github.com/iris-contrib/middleware/prometheus"
	"github.com/kataras/iris/v12"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func EnablePrometheusMiddleware(app *iris.Application) {
	m := prometheus.New("Metrics", 0.3, 1.2, 5.0)
	app.Use(m.ServeHTTP)
	app.OnAnyErrorCode(func(ctx iris.Context) {
		// error code handlers are not sharing the same middleware as other routes, so we have
		// to call them inside their body.
		m.ServeHTTP(ctx)
	})
}

func ServeMetrics(port string) {
	metrics := iris.Default()

	l.AddGoLogSource(metrics.Logger().SetOutput)
	metrics.Get("/metrics", iris.FromStd(promhttp.Handler()))
	metrics.Get("/debug/pprof", iris.FromStd(pprof.Index))
	metrics.Get("/debug/pprof/cmdline", iris.FromStd(pprof.Cmdline))
	metrics.Get("/debug/pprof/profile", iris.FromStd(pprof.Profile))
	metrics.Get("/debug/pprof/symbol", iris.FromStd(pprof.Symbol))

	metrics.Get("/debug/pprof/goroutine", iris.FromStd(pprof.Handler("goroutine")))
	metrics.Get("/debug/pprof/heap", iris.FromStd(pprof.Handler("heap")))
	metrics.Get("/debug/pprof/threadcreate", iris.FromStd(pprof.Handler("threadcreate")))
	metrics.Get("/debug/pprof/block", iris.FromStd(pprof.Handler("block")))

	err := metrics.Build()
	if err != nil {
		panic(err)
	}
	metricsServer := &http.Server{Addr: ":" + port, Handler: metrics}

	go func() {
		err := metricsServer.ListenAndServe()
		if err != nil {
			l.LogE("Server shutdown", err)
		}
	}()
}