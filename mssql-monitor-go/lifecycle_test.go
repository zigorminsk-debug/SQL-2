package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type blockingDB struct {
	started chan struct{}
	active  atomic.Int32
	closed  atomic.Bool
}

func (b *blockingDB) Query(string) ([]map[string]any, error) {
	return nil, errors.New("expected contextual query")
}
func (b *blockingDB) QueryContext(ctx context.Context, q string) ([]map[string]any, error) {
	b.active.Add(1)
	defer b.active.Add(-1)
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (b *blockingDB) DriverName() string { return "fake" }
func (b *blockingDB) ServerName() string { return "fake" }
func (b *blockingDB) Close()             { b.closed.Store(true) }
func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not finish")
	}
}
func TestShutdownCancelsMonitorAndReport(t *testing.T) {
	cfg := defaultConfig()
	a := NewApp(&cfg, &Logger{})
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.store = st
	db := &blockingDB{started: make(chan struct{}, 4)}
	a.Connect(db)
	a.handleMonitorStart(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
	await(t, db.started)
	a.runReport("test")
	await(t, db.started)
	done := make(chan struct{})
	go func() { a.Shutdown(); close(done) }()
	await(t, done)
	a.Shutdown()
	if a.monitor.Alive() || !db.closed.Load() || db.active.Load() != 0 || a.Connected() != nil {
		t.Fatal("live resources after shutdown")
	}
	a.reportMu.Lock()
	running := a.reportRun
	path := a.reportPath
	a.reportMu.Unlock()
	if running || path != "" {
		t.Fatal("report was published or still running")
	}
	if a.beginWork() {
		t.Fatal("accepted work after shutdown")
	}
	if n, _, _ := st.SampleCount(); n != 0 {
		t.Fatal("cancelled tick persisted")
	}
}
func TestExitValidationAndResponse(t *testing.T) {
	for _, tc := range []struct {
		method, origin, ct string
		want               int
	}{{"GET", "", "", 405}, {"POST", "", "text/plain", 415}, {"POST", "https://evil.example", "application/json", 403}} {
		cfg := defaultConfig()
		a := NewApp(&cfg, &Logger{})
		req := httptest.NewRequest(tc.method, "http://monitor.local/api/exit", strings.NewReader("{}"))
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Content-Type", tc.ct)
		w := httptest.NewRecorder()
		a.handleExit(w, req)
		if w.Code != tc.want || a.ctx.Err() != nil {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
		a.Shutdown()
	}
	cfg := defaultConfig()
	a := NewApp(&cfg, &Logger{})
	req := httptest.NewRequest("POST", "http://monitor.local/api/exit", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handleExit(w, req)
	await(t, a.exitCh)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatal(w.Body.String())
	}
}
func TestExitStopsHTTPListener(t *testing.T) {
	cfg := defaultConfig()
	a := NewApp(&cfg, &Logger{})
	port, err := a.StartWebServer("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	url := "http://127.0.0.1:" + fmt.Sprint(port)
	client := &http.Client{Timeout: 3 * time.Second}
	r, err := client.Post(url+"/api/exit", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	await(t, a.exitCh)
	if r, err := client.Get(url + "/api/state"); err == nil {
		r.Body.Close()
		t.Fatal("HTTP listener still running")
	}
}

func TestManualStopDoesNotStartAutoReport(t *testing.T) {
	cfg := defaultConfig()
	a := NewApp(&cfg, &Logger{})
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.store = st
	if err := st.AppendSample(Sample{TS: 123}); err != nil {
		t.Fatal(err)
	}
	db := &blockingDB{started: make(chan struct{}, 4)}
	a.Connect(db)
	a.handleMonitorStart(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
	await(t, db.started)
	a.StopMonitor(true)
	a.reportMu.Lock()
	running := a.reportRun
	a.reportMu.Unlock()
	if running {
		t.Fatal("auto report started on manual stop")
	}
	if a.monitor.Alive() {
		t.Fatal("StopMonitor returned before completion")
	}
	if n, _, _ := st.SampleCount(); n != 1 {
		t.Fatal("existing data lost")
	}
	a.Shutdown()
}
