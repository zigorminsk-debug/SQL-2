// monitor.go — фоновый движок суточного мониторинга.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type MonitorEngine struct {
	mu         sync.Mutex
	db         Querier
	store      *Store
	interval   time.Duration
	duration   time.Duration
	longQueryS float64

	ctx     context.Context
	cancel  context.CancelFunc
	stopCh  chan struct{}
	doneCh  chan struct{}
	stopped bool

	Samples int
	Errors  int
	Started time.Time

	OnSample func(Sample)
	OnLog    func(string)
	OnDone   func(*MonitorEngine)
}

func NewMonitorEngine(db Querier, store *Store, interval, duration time.Duration,
	longQueryS float64) *MonitorEngine {
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &MonitorEngine{db: scopedQuerier{db, ctx}, ctx: ctx, cancel: cancel, store: store, interval: interval,
		duration: duration, longQueryS: longQueryS,
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
		Started: time.Now()}
}

func (e *MonitorEngine) logf(format string, a ...any) {
	if e.OnLog != nil {
		e.OnLog(fmt.Sprintf(format, a...))
	}
}

func (e *MonitorEngine) Start() {
	go func() {
		defer close(e.doneCh)
		e.logf("Мониторинг запущен: интервал %s, длительность %s, сервер: %s",
			e.interval, e.duration, e.db.ServerName())
		end := time.Now().Add(e.duration)
		var prev *TickState
		lastLong := map[int]float64{} // session_id -> последний зафиксированный elapsed
		for {
			if e.ctx.Err() != nil {
				e.finish(true)
				return
			}
			sm, blocked, longs, state, err := collectLightSample(e.db, prev, e.longQueryS)
			if e.ctx.Err() != nil {
				e.finish(true)
				return
			}
			if err != nil {
				e.mu.Lock()
				e.Errors++
				e.mu.Unlock()
				e.logf("Ошибка опроса: %v", err)
			} else {
				prev = state
				e.store.AppendSample(sm)
				if len(blocked) > 0 {
					e.store.AppendBlocking(blocked)
				}
				var newLong []LongQuery
				for _, q := range longs {
					if last, ok := lastLong[q.SessionID]; !ok || q.ElapsedS >= last+60 {
						newLong = append(newLong, q)
						lastLong[q.SessionID] = q.ElapsedS
					}
				}
				if len(newLong) > 0 {
					e.store.AppendLong(newLong)
				}
				e.mu.Lock()
				e.Samples++
				n := e.Samples
				e.mu.Unlock()
				sm.N = n
				if e.OnSample != nil {
					e.OnSample(sm)
				}
			}
			// до следующего тика
			wake := time.Now().Add(e.interval)
			if wake.After(end) {
				wake = end
			}
			timer := time.NewTimer(time.Until(wake))
			select {
			case <-e.stopCh:
				timer.Stop()
				e.finish(true)
				return
			case <-timer.C:
			}
			if !time.Now().Before(end) {
				break
			}
		}
		e.finish(false)
	}()
}

func (e *MonitorEngine) finish(byUser bool) {
	reason := "время истекло"
	if byUser {
		reason = "остановлен пользователем"
	}
	e.mu.Lock()
	n := e.Samples
	e.mu.Unlock()
	e.logf("Мониторинг завершён (%s). Собрано тиков: %d", reason, n)
	if e.OnDone != nil {
		e.OnDone(e)
	}
}

func (e *MonitorEngine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.stopped {
		e.stopped = true
		e.cancel()
		close(e.stopCh)
	}
}

// Alive — работает ли мониторинг.
func (e *MonitorEngine) Alive() bool {
	select {
	case <-e.doneCh:
		return false
	default:
		return true
	}
}

func (e *MonitorEngine) Wait()         { <-e.doneCh }
func (e *MonitorEngine) Stopped() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.stopped }
