// types.go — общие типы данных и утилиты для работы со строками DMV.
package main

import (
	"strconv"
	"strings"
	"time"
)

// Querier — единый интерфейс доступа к SQL Server (реальный или демо).
type Querier interface {
	Query(sql string) ([]map[string]any, error)
	DriverName() string
	ServerName() string
	Close()
}

// ---------------- утилиты чтения строк DMV ----------------

func gf(m map[string]any, k string) (float64, bool) {
	v, ok := m[k]
	if !ok || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case time.Time:
		return float64(x.Unix()), true
	}
	return 0, false
}

func gfi(m map[string]any, k string) float64 { f, _ := gf(m, k); return f }

func gfPtr(m map[string]any, k string) *float64 {
	if f, ok := gf(m, k); ok {
		return &f
	}
	return nil
}

func gi(m map[string]any, k string) (int, bool) {
	f, ok := gf(m, k)
	return int(f), ok
}

func gii(m map[string]any, k string) int { i, _ := gi(m, k); return i }

func gs(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	switch x := any(m[k]).(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case time.Time:
		return x.Format("02.01.2006 15:04:05")
	}
	return ""
}

func gtime(m map[string]any, k string) (time.Time, bool) {
	if t, ok := m[k].(time.Time); ok {
		return t, true
	}
	return time.Time{}, false
}

func hoursAgo(m map[string]any, k string) (float64, bool) {
	if t, ok := gtime(m, k); ok {
		return time.Since(t).Hours(), true
	}
	return 0, false
}

func strHead(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func queryRow(q Querier, sql string) map[string]any {
	rows, err := q.Query(sql)
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func queryAny(q Querier, sqls []string) ([]map[string]any, error) {
	var lastErr error
	for _, s := range sqls {
		rows, err := q.Query(s)
		if err == nil {
			return rows, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errEmpty
	}
	return nil, lastErr
}

var errEmpty = &simpleErr{"пустой запрос"}

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

// percentile по отсортированному срезу
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	k := int(p*float64(len(sorted)-1) + 0.5)
	if k < 0 {
		k = 0
	}
	if k >= len(sorted) {
		k = len(sorted) - 1
	}
	return sorted[k]
}

// ---------------- типы мониторинга ----------------

type WaitDelta struct {
	Type     string  `json:"type"`
	WaitMS   float64 `json:"wait_ms"`
	Tasks    float64 `json:"tasks"`
	SignalMS float64 `json:"signal_ms"`
}

type Sample struct {
	TS              int64       `json:"ts"`
	N               int         `json:"n,omitempty"`
	SQLCPU          *float64    `json:"sql_cpu,omitempty"`
	SysCPU          *float64    `json:"sys_cpu,omitempty"`
	BatchReq        *float64    `json:"batch_req,omitempty"`
	TransSec        *float64    `json:"trans_sec,omitempty"`
	CompilesSec     *float64    `json:"compiles_sec,omitempty"`
	RecompilesSec   *float64    `json:"recompiles_sec,omitempty"`
	LazyWrites      *float64    `json:"lazy_writes,omitempty"`
	PLE             *int        `json:"ple,omitempty"`
	BCHR            *float64    `json:"bchr,omitempty"`
	FullScans       *float64    `json:"full_scans,omitempty"`
	IndexSearches   *float64    `json:"index_searches,omitempty"`
	MemTargetMB     *float64    `json:"mem_target_mb,omitempty"`
	MemTotalMB      *float64    `json:"mem_total_mb,omitempty"`
	IORdLat         *float64    `json:"io_rd_lat,omitempty"`
	IOWrLat         *float64    `json:"io_wr_lat,omitempty"`
	IORdMBps        *float64    `json:"io_rd_mbps,omitempty"`
	IOWrMBps        *float64    `json:"io_wr_mbps,omitempty"`
	TempDBUsedMB    *float64    `json:"tempdb_used_mb,omitempty"`
	TempDBVersionMB *float64    `json:"tempdb_version_mb,omitempty"`
	Sessions        *int        `json:"sessions,omitempty"`
	Blocked         int         `json:"blocked"`
	BlockedMaxWait  float64     `json:"blocked_max_wait"`
	Waits           []WaitDelta `json:"waits"`
}

func (s *Sample) cpu() float64 {
	if s.SQLCPU != nil {
		return *s.SQLCPU
	}
	return 0
}

type BlockingEvent struct {
	TS                int64   `json:"ts"`
	SessionID         int     `json:"session_id"`
	BlockingSessionID int     `json:"blocking_session_id"`
	WaitType          string  `json:"wait_type"`
	WaitTimeS         float64 `json:"wait_time_s"`
	WaitResource      string  `json:"wait_resource"`
	Command           string  `json:"command"`
	Login             string  `json:"login"`
	Host              string  `json:"host"`
	Program           string  `json:"program"`
	DB                string  `json:"db"`
	ElapsedS          float64 `json:"elapsed_s"`
	TextHead          string  `json:"text_head"`
}

type LongQuery struct {
	TS           int64   `json:"ts"`
	SessionID    int     `json:"session_id"`
	Login        string  `json:"login"`
	Program      string  `json:"program"`
	DB           string  `json:"db"`
	ElapsedS     float64 `json:"elapsed_s"`
	CPUS         float64 `json:"cpu_s"`
	LogicalReads int64   `json:"logical_reads"`
	Writes       int64   `json:"writes"`
	Command      string  `json:"command"`
	TextHead     string  `json:"text_head"`
	WaitType     string  `json:"wait_type"`
}

// ---------------- типы глубокой диагностики ----------------

type WaitRow struct {
	Type     string
	WaitMS   float64
	Tasks    float64
	SignalMS float64
	MaxMS    float64
}

type WaitStatsData struct {
	Rows     []WaitRow
	TotalMS  float64
	SignalMS float64
	Tasks    float64
}

type LiveData struct {
	Requests []map[string]any
	Blockers []map[string]any
}

type QStatsData struct {
	ByCPU    []map[string]any
	ByAvg    []map[string]any
	ByReads  []map[string]any
	DupPlans []map[string]any
}

type ConvertInfo struct {
	Issue string
	Expr  string
	SQL   string
}

type PlanIssuesData struct {
	Converts       []ConvertInfo
	Spills         []ConvertInfo
	NoJoin         int
	MissingInPlans int
}

type DBsData struct {
	Databases []map[string]any
	Files     []map[string]any
}

type TempdbData struct {
	Files      []map[string]any
	Space      map[string]any
	SnapshotTR []map[string]any
}

type DeadlockProc struct {
	ID, Spid, Hostname, Loginname, Clientapp, Isolation,
	Waitresource, Waittime, Logused, CurrentDB,
	LastBatch, Inputbuf string
	Victim bool
}

type DeadlockInfo struct {
	Time      *time.Time
	Victim    string
	Processes []DeadlockProc
	Objects   []string
}

type DeepData struct {
	Workload       string
	FileVolumes    []map[string]any
	Server         map[string]any
	SysInfo        map[string]any
	Configs        []map[string]any
	Clerks         []map[string]any
	PlanCache      []map[string]any
	WaitStats      *WaitStatsData
	Live           *LiveData
	QStats         *QStatsData
	PlanIssues     *PlanIssuesData
	MissingIdx     []map[string]any
	Frag           []map[string]any
	Stats          []map[string]any
	UnusedIdx      []map[string]any
	DBs            *DBsData
	IOFiles        []map[string]any
	Volumes        []map[string]any
	Tempdb         *TempdbData
	TraceFlags     []map[string]any
	QueryStore     []map[string]any
	Backups        []map[string]any
	JobsFailed     []map[string]any
	Deadlocks      []DeadlockInfo
	ErrorLog       []map[string]any
	Errors         map[string]string
	BlockingEvents []BlockingEvent
	LongQueries    []LongQuery
}

type CheckOptions struct {
	Workload      string
	Fragmentation bool
	ErrorLog      bool
	PlanXML       bool
}

type Finding struct {
	ID       string
	Severity string // critical | warning | info
	Cat      string
	Title    string
	Evidence []string
	Why      string
	Action   string
	SQLT     string
	Num      int
}

type WaitAgg struct {
	Type string
	MS   float64
}

type PeakWindow struct {
	T0, T1    int64
	N         int
	CPUAvg    float64
	CPUMax    float64
	CPUMaxSet bool
	BatchAvg  float64
	PLEMin    float64
	PLEMinSet bool
	Waits     []WaitAgg
	WaitCats  map[string]float64
	Blocked   []BlockingEvent
	Long      []LongQuery
	Reasons   []string
}

type Health struct {
	Score int    `json:"score"`
	Grade string `json:"grade"`
	Crit  int    `json:"crit"`
	Warn  int    `json:"warn"`
	Info  int    `json:"info"`
}

// unixFmt — форматирование unix-времени в локальной зоне.
func unixFmt(ts int64, layout string) string {
	return time.Unix(ts, 0).Format(layout)
}
