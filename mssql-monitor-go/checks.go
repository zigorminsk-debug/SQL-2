// checks.go — сбор данных с SQL Server (только чтение системных представлений).
package main

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// benign-ожидания (не характеризуют проблемы)
var benignWaits = map[string]bool{
	"CXCONSUMER": true,
	"WAITFOR":    true, "SLEEP_TASK": true, "SLEEP_BPOOL_FLUSH": true,
	"SLEEP_DBSTARTUP": true, "SLEEP_DCOMSTARTUP": true, "SLEEP_MASTERDBREADY": true,
	"SLEEP_MASTERMDREADY": true, "SLEEP_MASTERUPGRADED": true, "SLEEP_MSDBSTARTUP": true,
	"SLEEP_SYSTEMTASK": true, "SLEEP_TEMPDBSTARTUP": true, "BROKER_TASK_STOP": true,
	"BROKER_TO_FLUSH": true, "BROKER_EVENTHANDLER": true, "BROKER_RECEIVE_WAITFOR": true,
	"BROKER_TRANSMITTER": true, "BROKER_SHUTDOWN": true, "CHECKPOINT_QUEUE": true,
	"CHKPT": true, "DIRTY_PAGE_POLL": true, "DISPATCHER_QUEUE_SEMAPHORE": true,
	"EXECSYNC": true, "FT_IFTSHC_MUTEX": true, "HTTP_ENDPOINT_COLLCREATE": true,
	"HTTP_ENUMERATION": true, "HTTP_START": true, "LAZYWRITER_SLEEP": true,
	"LOGMGR_QUEUE": true, "MIRROR_SEND": true, "MISCELLANEOUS": true,
	"ONDEMAND_TASK_QUEUE": true, "PARALLEL_REDO_DRAIN_TRANS": true,
	"PARALLEL_REDO_LOG_BUFFER": true, "PARALLEL_REDO_WORKER_WAIT": true,
	"PREEMPTIVE_OS_GETDISKFREESPACE": true, "PREEMPTIVE_OS_LIBRARYOPS": true,
	"PREEMPTIVE_OS_QUERYREGISTRY": true, "PREEMPTIVE_OS_WRITEFILEGATHER": true,
	"QDS_ASYNC_QUEUE": true, "QDS_CLEANUP_STALE_QUERIES_TASK_MAIN_LOOP_SLEEP": true,
	"QDS_PERSIST_TASK_MAIN_LOOP_SLEEP": true, "QDS_SHUTDOWN_QUEUE": true,
	"REDO_THREAD_PENDING_WORK": true, "RESOURCE_QUEUE": true, "SERVER_IDLE_CHECK": true,
	"SOS_WORK_DISPATCHER": true, "SP_SERVER_DIAGNOSTICS_SLEEP": true,
	"SQLTRACE_BUFFER_FLUSH": true, "SQLTRACE_INCREMENTAL_FLUSH_SLEEP": true,
	"SQLTRACE_WAIT_ENTRIES": true, "TRACEWRITE": true, "WAIT_FOR_RESULTS": true,
	"WAIT_XTP_OFFLINE_CKPT_NEW_LOG": true, "WAIT_XTP_RECOVERY": true,
	"VDI_CLIENT_OTHER": true, "XE_DISPATCHER_WAIT": true, "XE_LIVE_TARGET_TVF": true,
	"XE_TIMER_EVENT": true, "DIRTY_PAGE_TABLE_SYNC": true,
	"HADR_FILESTREAM_IOMGR_IOCOMPLETION": true, "DEADLOCK_ENUM_MUTEX": true,
	"SOSHOST_PUBSUB": true, "HADR_DB_SEEDING": true,
}

const sqlRequests = `
SELECT r.session_id, r.status, r.blocking_session_id, r.wait_type, r.wait_time,
       r.wait_resource, r.cpu_time, r.total_elapsed_time, r.reads, r.writes,
       r.logical_reads, r.command, r.percent_complete,
       DB_NAME(r.database_id) AS db_name,
       s.login_name, s.host_name, s.program_name, s.status AS session_status,
       t.text AS sql_text,
       CASE WHEN t.objectid IS NOT NULL
            THEN OBJECT_SCHEMA_NAME(t.objectid, t.dbid) + N'.' + OBJECT_NAME(t.objectid, t.dbid)
            ELSE NULL END AS object_name
FROM sys.dm_exec_requests r WITH (NOLOCK)
LEFT JOIN sys.dm_exec_sessions s WITH (NOLOCK) ON s.session_id = r.session_id
OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
WHERE r.session_id >= 50 OR r.blocking_session_id <> 0`

const sqlPerf = `
SELECT RTRIM(LTRIM(object_name)) AS object_name,
       RTRIM(LTRIM(counter_name)) AS counter_name,
       RTRIM(LTRIM(instance_name)) AS instance_name,
       cntr_value, cntr_type
FROM sys.dm_os_performance_counters
WHERE counter_name IN (
  N'Batch Requests/sec', N'Transactions/sec', N'SQL Compilations/sec',
  N'SQL Re-Compilations/sec', N'Page life expectancy',
  N'Buffer cache hit ratio', N'Buffer cache hit ratio base',
  N'Lazy Writes/sec', N'Full Scans/sec', N'Index Searches/sec',
  N'Target Server Memory (KB)', N'Total Server Memory (KB)',
  N'Lock Wait Time (ms)')`

const sqlWaits = `
SELECT wait_type, waiting_tasks_count, wait_time_ms, signal_wait_time_ms
FROM sys.dm_os_wait_stats
WHERE wait_time_ms > 0`

const sqlIO = `
SELECT database_id, SUM(CAST(num_of_reads AS BIGINT)) AS r,
       SUM(CAST(io_stall_read_ms AS BIGINT)) AS r_ms,
       SUM(CAST(num_of_writes AS BIGINT)) AS w,
       SUM(CAST(io_stall_write_ms AS BIGINT)) AS w_ms,
       SUM(CAST(num_of_bytes_read AS BIGINT)) AS rb,
       SUM(CAST(num_of_bytes_written AS BIGINT)) AS wb
FROM sys.dm_io_virtual_file_stats(NULL, NULL)
GROUP BY database_id`

const sqlTempDB = `
SELECT SUM(CAST(user_object_reserved_page_count AS BIGINT))*8/1024.0 AS user_mb,
       SUM(CAST(internal_object_reserved_page_count AS BIGINT))*8/1024.0 AS internal_mb,
       SUM(CAST(version_store_reserved_page_count AS BIGINT))*8/1024.0 AS version_mb,
       SUM(CAST(unallocated_extent_page_count AS BIGINT))*8/1024.0 AS free_mb
FROM tempdb.sys.dm_db_file_space_usage`

const sqlSessions = `
SELECT COUNT(*) AS cnt FROM sys.dm_exec_sessions WITH (NOLOCK) WHERE is_user_process = 1`

const sqlCPURB = `
SELECT TOP 1 CAST(record AS NVARCHAR(2048)) AS rec
FROM sys.dm_os_ring_buffers WITH (NOLOCK)
WHERE ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR'
ORDER BY timestamp DESC`

var reProcUtil = regexp.MustCompile(`<ProcessUtilization>(-?\d+)</ProcessUtilization>`)
var reSysIdle = regexp.MustCompile(`<SystemIdle>(-?\d+)</SystemIdle>`)

// TickState — кумулятивные значения предыдущего тика (для дельт).
type TickState struct {
	TS       int64
	PerfVals map[string]float64
	Waits    map[string][3]float64
	IO       []map[string]any
}

func perfVal(rows []map[string]any, like, counter, inst string) (float64, bool) {
	for _, r := range rows {
		obj := strings.ToLower(gs(r, "object_name"))
		cnt := strings.ToLower(gs(r, "counter_name"))
		in := strings.ToLower(gs(r, "instance_name"))
		if cnt != strings.ToLower(counter) {
			continue
		}
		if !strings.Contains(obj, strings.ToLower(like)) {
			continue
		}
		if inst != "" && in != strings.ToLower(inst) {
			continue
		}
		return gfi(r, "cntr_value"), true
	}
	return 0, false
}

// ============================================================
// ЛЁГКИЙ ОПРОС (мониторинг)
// ============================================================
// Возвращает: тик, события блокировок, долгие запросы, новое состояние.
func collectLightSample(db Querier, prev *TickState, longQueryS float64) (
	Sample, []BlockingEvent, []LongQuery, *TickState, error) {
	s := Sample{TS: time.Now().Unix()}
	var blocked []BlockingEvent
	var longs []LongQuery

	// CPU из кольцевого буфера (мгновенное значение)
	if row := queryRow(db, sqlCPURB); row != nil {
		rec := gs(row, "rec")
		if m := reProcUtil.FindStringSubmatch(rec); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			s.SQLCPU = &v
		}
		if m := reSysIdle.FindStringSubmatch(rec); m != nil {
			idle, _ := strconv.ParseFloat(m[1], 64)
			v := 100 - idle
			s.SysCPU = &v
		}
	}

	perf, err := db.Query(sqlPerf)
	if err != nil {
		perf = nil
	}
	state := &TickState{TS: s.TS, PerfVals: map[string]float64{}}

	dt := 0.0
	if prev != nil {
		dt = float64(s.TS - prev.TS)
	}

	get := func(like, counter, inst string) (float64, bool) {
		return perfVal(perf, like, counter, inst)
	}
	if v, ok := get("Buffer Manager", "Page life expectancy", ""); ok {
		p := int(v)
		s.PLE = &p
	}
	if v, ok := get("Memory Manager", "Target Server Memory (KB)", ""); ok {
		x := math.Round(v/1024*10) / 10
		s.MemTargetMB = &x
	}
	if v, ok := get("Memory Manager", "Total Server Memory (KB)", ""); ok {
		x := math.Round(v/1024*10) / 10
		s.MemTotalMB = &x
	}

	cur := map[string]float64{}
	for key, spec := range map[string][3]string{
		"batch":    {"SQL Statistics", "Batch Requests/sec", ""},
		"trans":    {"Databases", "Transactions/sec", "_Total"},
		"comp":     {"SQL Statistics", "SQL Compilations/sec", ""},
		"recomp":   {"SQL Statistics", "SQL Re-Compilations/sec", ""},
		"lazy":     {"Buffer Manager", "Lazy Writes/sec", ""},
		"scan":     {"Access Methods", "Full Scans/sec", ""},
		"isearch":  {"Access Methods", "Index Searches/sec", ""},
		"bc":       {"Buffer Manager", "Buffer cache hit ratio", ""},
		"bcb":      {"Buffer Manager", "Buffer cache hit ratio base", ""},
		"lockwait": {"Locks", "Lock Wait Time (ms)", "_Total"},
	} {
		if v, ok := get(spec[0], spec[1], spec[2]); ok {
			cur[key] = v
			state.PerfVals[key] = v
		}
	}
	if prev != nil && dt > 0 {
		rate := func(key string) *float64 {
			cv, cok := cur[key]
			pv, pok := prev.PerfVals[key]
			if !cok || !pok || cv-pv < 0 {
				return nil
			}
			x := (cv - pv) / dt
			return &x
		}
		s.BatchReq = rate("batch")
		s.TransSec = rate("trans")
		s.CompilesSec = rate("comp")
		s.RecompilesSec = rate("recomp")
		s.LazyWrites = rate("lazy")
		s.FullScans = rate("scan")
		s.IndexSearches = rate("isearch")
		if cv, cok := cur["bc"]; cok {
			if cbv, cbok := cur["bcb"]; cbok {
				if pv, pok := prev.PerfVals["bc"]; pok {
					if pbv, pbok := prev.PerfVals["bcb"]; pbok && cbok {
						dv, dbase := cv-pv, cbv-pbv
						if dv > 0 && dbase > 0 {
							x := math.Round(100*dv/dbase*100) / 100
							s.BCHR = &x
						}
					}
				}
			}
		}
	}

	// ожидания (дельта)
	if rows, err := db.Query(sqlWaits); err == nil {
		curW := map[string][3]float64{}
		for _, r := range rows {
			wt := gs(r, "wait_type")
			curW[wt] = [3]float64{gfi(r, "wait_time_ms"), gfi(r, "waiting_tasks_count"),
				gfi(r, "signal_wait_time_ms")}
		}
		state.Waits = curW
		if prev != nil && prev.Waits != nil && dt > 0 {
			deltas := make([]WaitDelta, 0)
			for wt, v := range curW {
				if benignWaits[wt] {
					continue
				}
				p, ok := prev.Waits[wt]
				if !ok {
					continue
				}
				dw := v[0] - p[0]
				if dw <= 0 {
					continue
				}
				deltas = append(deltas, WaitDelta{Type: wt,
					WaitMS:   math.Round(dw/1000*1000) / 1000,
					Tasks:    math.Max(0, v[1]-p[1]),
					SignalMS: math.Round(math.Max(0, v[2]-p[2])/1000*1000) / 1000})
			}
			sort.Slice(deltas, func(i, j int) bool { return deltas[i].WaitMS > deltas[j].WaitMS })
			s.Waits = deltas
		}
	}

	// ввод-вывод (дельта)
	if rows, err := db.Query(sqlIO); err == nil {
		state.IO = rows
		if prev != nil && prev.IO != nil && dt > 0 {
			sum := func(rows []map[string]any, k string) float64 {
				t := 0.0
				for _, r := range rows {
					t += gfi(r, k)
				}
				return t
			}
			r_ := sum(rows, "r") - sum(prev.IO, "r")
			rms := sum(rows, "r_ms") - sum(prev.IO, "r_ms")
			w_ := sum(rows, "w") - sum(prev.IO, "w")
			wms := sum(rows, "w_ms") - sum(prev.IO, "w_ms")
			rb := sum(rows, "rb") - sum(prev.IO, "rb")
			wb := sum(rows, "wb") - sum(prev.IO, "wb")
			if r_ > 0 {
				x := math.Round(rms/r_*100) / 100
				s.IORdLat = &x
			}
			if w_ > 0 {
				x := math.Round(wms/w_*100) / 100
				s.IOWrLat = &x
			}
			x1 := math.Round(rb/dt/1048576*100) / 100
			s.IORdMBps = &x1
			x2 := math.Round(wb/dt/1048576*100) / 100
			s.IOWrMBps = &x2
		}
	}

	// tempdb
	if row := queryRow(db, sqlTempDB); row != nil {
		x := math.Round((gfi(row, "user_mb")+gfi(row, "internal_mb"))*10) / 10
		s.TempDBUsedMB = &x
		v := math.Round(gfi(row, "version_mb")*10) / 10
		s.TempDBVersionMB = &v
	}

	// сессии
	if row := queryRow(db, sqlSessions); row != nil {
		if v, ok := gi(row, "cnt"); ok {
			s.Sessions = &v
		}
	}

	// активные запросы, блокировки, долгие запросы
	maxWait := 0.0
	if rows, err := db.Query(sqlRequests); err == nil {
		for _, r := range rows {
			b := gii(r, "blocking_session_id")
			elapsed := gfi(r, "total_elapsed_time") / 1000.0
			if b != 0 && b > -2 {
				s.Blocked++
				w := gfi(r, "wait_time") / 1000.0
				if w > maxWait {
					maxWait = w
				}
				blocked = append(blocked, BlockingEvent{
					TS: s.TS, SessionID: gii(r, "session_id"), BlockingSessionID: b,
					WaitType:     gs(r, "wait_type"),
					WaitTimeS:    math.Round(gfi(r, "wait_time")/1000*10) / 10,
					WaitResource: gs(r, "wait_resource"), Command: gs(r, "command"),
					Login: gs(r, "login_name"), Host: gs(r, "host_name"),
					Program: gs(r, "program_name"), DB: gs(r, "db_name"),
					ElapsedS: math.Round(elapsed*10) / 10,
					TextHead: strHead(gs(r, "sql_text"), 500)})
			}
			if gii(r, "session_id") >= 50 && elapsed >= longQueryS {
				longs = append(longs, LongQuery{
					TS: s.TS, SessionID: gii(r, "session_id"),
					Login: gs(r, "login_name"), Program: gs(r, "program_name"),
					DB: gs(r, "db_name"), ElapsedS: math.Round(elapsed*10) / 10,
					CPUS:         math.Round(gfi(r, "cpu_time")/1000*10) / 10,
					LogicalReads: int64(gfi(r, "logical_reads")),
					Writes:       int64(gfi(r, "writes")),
					Command:      gs(r, "command"),
					TextHead:     strHead(gs(r, "sql_text"), 500),
					WaitType:     gs(r, "wait_type")})
			}
		}
		s.BlockedMaxWait = math.Round(maxWait*10) / 10
	}
	return s, blocked, longs, state, nil
}
