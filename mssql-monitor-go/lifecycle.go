package main

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// beginWork and shutdown share a gate: no WaitGroup.Add may race with Wait.
func (a *App) beginWork() bool {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	if a.closing {
		return false
	}
	a.work.Add(1)
	return true
}

func (a *App) apiHandler(next http.HandlerFunc, mutate bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mutate {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				writeErr(w, 405, "требуется POST")
				return
			}
			// Reject cross-origin browser shutdown/configuration requests.
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					writeErr(w, 403, "чужой Origin")
					return
				}
			}
			if r.Header.Get("Content-Type") != "application/json" {
				writeErr(w, 415, "требуется application/json")
				return
			}
		}
		if !a.beginWork() {
			writeErr(w, 503, "программа завершается")
			return
		}
		defer a.work.Done()
		next(w, r)
	}
}

func (a *App) stopWork() {
	a.stopOnce.Do(func() {
		a.lifeMu.Lock()
		a.closing = true
		a.cancel()
		a.lifeMu.Unlock()
		a.StopMonitor(false)
		a.work.Wait()
		// An already accepted connect/start request could finish during cancellation.
		a.StopMonitor(false)
		a.Connect(nil)
		a.log.Infof("Все задачи мониторинга и диагностики завершены, соединения закрыты")
	})
}

// Shutdown is idempotent and also used on SIGINT/SIGTERM.
func (a *App) Shutdown() {
	a.stopWork()
	a.once.Do(func() {
		if a.httpServer != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := a.httpServer.Shutdown(ctx); err != nil {
				a.httpServer.Close()
			}
		}
		a.log.Close()
		close(a.exitCh)
	})
}

// scopedQuerier gives a monitor/report its own cancellation scope without
// closing a shared SQL pool used by other tasks.
type scopedQuerier struct {
	Querier
	ctx context.Context
}

func (q scopedQuerier) Query(sql string) ([]map[string]any, error) {
	if err := q.ctx.Err(); err != nil {
		return nil, err
	}
	if db, ok := q.Querier.(interface {
		QueryContext(context.Context, string) ([]map[string]any, error)
	}); ok {
		return db.QueryContext(q.ctx, sql)
	}
	return q.Querier.Query(sql)
}
