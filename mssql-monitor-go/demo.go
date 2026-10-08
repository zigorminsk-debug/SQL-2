// demo.go — демонстрационный режим без реального SQL Server (порт demo.py).
// DemoDatabase подменяет драйвер и отвечает реалистичными данными.
package main

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

func demoHour() float64 {
	n := time.Now()
	return float64(n.Hour()) + float64(n.Minute())/60.0 + float64(n.Second())/3600.0
}

func inH(h, a, b float64) bool {
	if a < b {
		return a <= h && h < b
	}
	return h >= a || h < b
}

// «дневная» нагрузка
func demoLoad(h float64) float64 {
	if inH(h, 9.6, 10.9) { // утренний пик (отчёты)
		return 1.0
	}
	if inH(h, 14.9, 15.6) { // послеобеденные блокировки
		return 0.75
	}
	if h >= 9 && h < 19 {
		return 0.6
	}
	if (h >= 7 && h < 9) || (h >= 19 && h < 22) {
		return 0.3
	}
	return 0.12 // ночь
}

func demoCPU(h float64) float64 {
	base := 8 + 55*demoLoad(h)
	if inH(h, 9.6, 10.9) {
		base += 18
	}
	d := 3.0
	if int(h*7)%2 == 0 {
		d = -3.0
	}
	v := base + d
	if v > 98 {
		v = 98
	}
	return v
}

func demoBatch(h float64) float64 { return 30 + 380*demoLoad(h) }

var demoSQLTexts = []string{
	"SELECT o.Id, o.Number, c.Name FROM dbo.Orders o JOIN dbo.Customers c ON " +
		"c.Id = o.CustomerId WHERE o.CreatedAt >= @1 AND o.Status = @2 ORDER BY o.CreatedAt DESC",
	"exec dbo.usp_CalcDailyReport @Date = '2026-09-30', @Region = 4",
	"UPDATE TOP (500) o SET o.Processed = 1, o.UpdatedBy = SUSER_SNAME() " +
		"FROM dbo.Orders o WHERE o.Processed = 0",
	"INSERT INTO dbo.ReportCache (KeyHash, Payload) VALUES (@k, @p)",
	"SELECT * FROM dbo.Events WHERE Name LIKE @p1 AND CreatedAt > @p2",
	"DELETE FROM dbo.Sessions WHERE ExpiresAt < GETDATE()",
}

const demoLogin = `CORP\srv-report`
const demoProg = ".Net SqlClient Data Provider"
const demoHost = "APP-SRV-07"

type DemoDatabase struct {
	t0      time.Time
	perfCum map[string]float64
	waitCum map[string]float64
}

func NewDemoDatabase() *DemoDatabase {
	return &DemoDatabase{
		t0: time.Now(),
		perfCum: map[string]float64{"batch": 4712000.0, "trans": 9200000.0, "comp": 402000.0,
			"recomp": 21000.0, "lazy": 88000.0, "scan": 129000.0,
			"isearch": 5230000.0, "lwait": 4410000.0},
		waitCum: map[string]float64{
			"PAGEIOLATCH_SH": 34 * 260000.0, "CXPACKET": 16 * 260000.0,
			"CXCONSUMER": 8 * 260000.0, "SOS_SCHEDULER_YIELD": 7 * 260000.0,
			"LCK_M_IX": 2.6 * 260000.0, "LCK_M_S": 1.4 * 260000.0,
			"WRITELOG": 7 * 260000.0, "ASYNC_NETWORK_IO": 6 * 260000.0,
			"RESOURCE_SEMAPHORE": 3 * 260000.0, "PAGELATCH_UP": 2 * 260000.0,
			"BACKUPIO": 1 * 260000.0, "OLEDB": 2 * 260000.0,
		},
	}
}

func (d *DemoDatabase) DriverName() string { return "демо-режим (без сервера)" }
func (d *DemoDatabase) ServerName() string { return `DEMO-SQL\SQL2022` }
func (d *DemoDatabase) Close()             {}

func demoAgo(d, h, m int) time.Time {
	return time.Now().AddDate(0, 0, -d).Add(-time.Duration(h)*time.Hour -
		time.Duration(m)*time.Minute)
}

func (d *DemoDatabase) verRow() map[string]any {
	return map[string]any{
		"srv": `DEMO-SQL\SQL2022`,
		"v": "Microsoft SQL Server 2022 (RTM-CU14) (KB5038325) - 16.0.4165.4 (X64) " +
			"Build 16.0.4165.4: Standard Edition (64-bit) on Windows Server 2019 " +
			"Standard 10.0 <X64> (Build 17763:)\n\t(c) Microsoft Corporation. " +
			"All rights reserved.\n",
		"inst": "SQL2022", "edition": "Standard Edition (64-bit)",
		"ver": "16.0.4165.4", "level": "RTM", "major": "16", "cu": "CU14",
		"machine": "DEMO-SQL", "clustered": float64(0),
		"collation": "Cyrillic_General_CI_AS"}
}

// Query — главная диспетчеризация демо-данных по подстрокам SQL.
func (d *DemoDatabase) Query(sql string) ([]map[string]any, error) {
	s := strings.TrimSpace(sql)
	U := strings.ToUpper(s)
	h := demoHour()

	// ---------- лёгкий опрос ----------
	if strings.Contains(U, "RING_BUFFER_SCHEDULER_MONITOR") {
		idle := 100 - int(demoCPU(h)*1.08)
		if idle < 1 {
			idle = 1
		}
		rec := fmt.Sprintf("<Record id='3' type='RING_BUFFER_SCHEDULER_MONITOR' "+
			"time='%d'><SchedulerMonitorEvent><System>"+
			"<ProcessUtilization>%d</ProcessUtilization>"+
			"<SystemIdle>%d</SystemIdle>"+
			"<MemoryUtilization>100</MemoryUtilization>"+
			"</System></SchedulerMonitorEvent></Record>",
			int(time.Now().Unix()), int(demoCPU(h)), idle)
		return []map[string]any{{"rec": rec}}, nil
	}
	if strings.Contains(U, "DM_OS_PERFORMANCE_COUNTERS") {
		dt := time.Since(d.t0).Seconds()
		if dt < 1 {
			dt = 1
		}
		d.t0 = time.Now()
		ld := demoLoad(h)
		comp := demoBatch(h) * 0.11
		if inH(h, 9.6, 10.9) {
			comp = demoBatch(h) * 0.22
		}
		lazy := 8.0
		if inH(h, 9.6, 10.9) {
			lazy = 45
		} else if ld <= 0.4 {
			lazy = 1
		}
		lwait := 120 * ld
		if inH(h, 14.9, 15.6) {
			lwait = 3500
		}
		rates := map[string]float64{
			"batch": demoBatch(h), "trans": demoBatch(h) * 3.2, "comp": comp,
			"recomp": demoBatch(h) * 0.012, "lazy": lazy,
			"scan": 25 + 300*ld, "isearch": 3000 + 22000*ld, "lwait": lwait,
		}
		for k, r := range rates {
			d.perfCum[k] += r * dt
		}
		c := d.perfCum
		ple := 540.0
		if inH(h, 9.6, 10.9) {
			ple = 265
		} else if demoLoad(h) < 0.2 {
			ple = 1620
		}
		bchr := 97.9
		if inH(h, 9.6, 10.9) {
			bchr = 94.3
		}
		rows := [][5]any{
			{"MSSQL$SQL2022:SQL Statistics", "Batch Requests/sec", "", c["batch"], float64(272696576)},
			{"MSSQL$SQL2022:Databases", "Transactions/sec", "_Total", c["trans"], float64(272696576)},
			{"MSSQL$SQL2022:SQL Statistics", "SQL Compilations/sec", "", c["comp"], float64(272696576)},
			{"MSSQL$SQL2022:SQL Statistics", "SQL Re-Compilations/sec", "", c["recomp"], float64(272696576)},
			{"MSSQL$SQL2022:Buffer Manager", "Page life expectancy", "", ple, float64(65792)},
			{"MSSQL$SQL2022:Buffer Manager", "Buffer cache hit ratio", "", bchr * 740, float64(1073874176)},
			{"MSSQL$SQL2022:Buffer Manager", "Buffer cache hit ratio base", "", 740.0, float64(1073874176)},
			{"MSSQL$SQL2022:Buffer Manager", "Lazy Writes/sec", "", c["lazy"], float64(272696576)},
			{"MSSQL$SQL2022:Access Methods", "Full Scans/sec", "", c["scan"], float64(272696576)},
			{"MSSQL$SQL2022:Access Methods", "Index Searches/sec", "", c["isearch"], float64(272696576)},
			{"MSSQL$SQL2022:Memory Manager", "Target Server Memory (KB)", "", float64(25165824), float64(65792)},
			{"MSSQL$SQL2022:Memory Manager", "Total Server Memory (KB)", "", float64(24510000), float64(65792)},
			{"MSSQL$SQL2022:Locks", "Lock Wait Time (ms)", "_Total", c["lwait"], float64(272696576)},
		}
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, map[string]any{"object_name": r[0], "counter_name": r[1],
				"instance_name": r[2], "cntr_value": r[3], "cntr_type": r[4]})
		}
		return out, nil
	}
	if strings.Contains(U, "DM_OS_WAIT_STATS") {
		profile := map[string]float64{"PAGEIOLATCH_SH": 34, "CXPACKET": 16, "CXCONSUMER": 8,
			"SOS_SCHEDULER_YIELD": 7, "LCK_M_IX": 2, "LCK_M_S": 2,
			"WRITELOG": 7, "ASYNC_NETWORK_IO": 6, "RESOURCE_SEMAPHORE": 3,
			"PAGELATCH_UP": 2, "BACKUPIO": 1, "OLEDB": 2}
		if inH(h, 14.9, 15.6) {
			profile = map[string]float64{"LCK_M_IX": 38, "LCK_M_S": 22, "PAGEIOLATCH_SH": 8,
				"CXPACKET": 6, "WRITELOG": 6, "SOS_SCHEDULER_YIELD": 4,
				"ASYNC_NETWORK_IO": 5}
		}
		if inH(h, 9.6, 10.9) {
			profile = map[string]float64{"PAGEIOLATCH_SH": 40, "CXPACKET": 20, "CXCONSUMER": 10,
				"SOS_SCHEDULER_YIELD": 9, "RESOURCE_SEMAPHORE": 5,
				"WRITELOG": 3, "ASYNC_NETWORK_IO": 4}
		}
		for k, share := range profile {
			cur, ok := d.waitCum[k]
			if !ok {
				cur = 1e6 + 1000*float64(len(k))
			}
			d.waitCum[k] = cur + share*800.0
		}
		out := []map[string]any{}
		base := map[string]float64{"WAITFOR": 1e9, "SLEEP_TASK": 1e9, "BROKER_TASK_STOP": 1e8,
			"DIRTY_PAGE_POLL": 1e8, "HADR_FILESTREAM_IOMGR_IOCOMPLETION": 1e7,
			"XE_TIMER_EVENT": 1e8, "LAZYWRITER_SLEEP": 1e9,
			"LOGMGR_QUEUE": 1e8, "CHECKPOINT_QUEUE": 1e8,
			"BROKER_TO_FLUSH": 1e8, "SOS_WORK_DISPATCHER": 1e8}
		for k, v := range base {
			out = append(out, map[string]any{"wait_type": k,
				"waiting_tasks_count": float64(int64(v / 100)),
				"wait_time_ms":        v, "signal_wait_time_ms": v * 0.02,
				"max_wait_time_ms": 5200.0})
		}
		for k, v := range d.waitCum {
			out = append(out, map[string]any{"wait_type": k,
				"waiting_tasks_count": float64(int64(v / 350)),
				"wait_time_ms":        v, "signal_wait_time_ms": v * 0.21,
				"max_wait_time_ms": 32000.0})
		}
		return out, nil
	}
	if strings.Contains(U, "DM_EXEC_REQUESTS") {
		return d.requests(h), nil
	}
	if strings.Contains(U, "DM_EXEC_SESSIONS") && strings.Contains(U, "COUNT(*)") {
		return []map[string]any{{"cnt": float64(int(38 + 26*demoLoad(h)))}}, nil
	}
	if strings.Contains(U, "DM_IO_VIRTUAL_FILE_STATS") && strings.Contains(U, "JOIN SYS.MASTER_FILES") {
		return []map[string]any{
			{"db": "SalesDB", "logical_name": "SalesDB_data", "type_desc": "ROWS",
				"num_of_reads": 920000000.0, "io_stall_read_ms": 31400000000.0,
				"num_of_writes": 41000000.0, "io_stall_write_ms": 590000000.0,
				"size_on_disk_bytes": 64e9, "physical_name": `E:\Data\SalesDB_data.mdf`},
			{"db": "SalesDB", "logical_name": "SalesDB_log", "type_desc": "LOG",
				"num_of_reads": 1200000.0, "io_stall_read_ms": 14000000.0,
				"num_of_writes": 210000000.0, "io_stall_write_ms": 2520000000.0,
				"size_on_disk_bytes": 15e9, "physical_name": `F:\Logs\SalesDB_log.ldf`},
			{"db": "ReportDB", "logical_name": "ReportDB", "type_desc": "ROWS",
				"num_of_reads": 143000000.0, "io_stall_read_ms": 4110000000.0,
				"num_of_writes": 8100000.0, "io_stall_write_ms": 129000000.0,
				"size_on_disk_bytes": 32e9, "physical_name": `E:\Data\ReportDB.mdf`},
			{"db": "WarehouseDB", "logical_name": "WH", "type_desc": "ROWS",
				"num_of_reads": 61000000.0, "io_stall_read_ms": 1450000000.0,
				"num_of_writes": 3100000.0, "io_stall_write_ms": 42000000.0,
				"size_on_disk_bytes": 107e9, "physical_name": `G:\WH\WH.mdf`},
			{"db": "tempdb", "logical_name": "tempdev", "type_desc": "ROWS",
				"num_of_reads": 41000000.0, "io_stall_read_ms": 520000000.0,
				"num_of_writes": 21000000.0, "io_stall_write_ms": 152000000.0,
				"size_on_disk_bytes": 8e9, "physical_name": `D:\TempDB\tempdev.mdf`},
		}, nil
	}
	if strings.Contains(U, "SOURCE_DATABASE_ID IS NULL") {
		return []map[string]any{{"name": "SalesDB"}, {"name": "ReportDB"},
			{"name": "WarehouseDB"}}, nil
	}
	if strings.Contains(U, "DM_IO_VIRTUAL_FILE_STATS") {
		return []map[string]any{
			{"database_id": 2.0, "r": 41000000.0, "r_ms": 520000000.0, "w": 21000000.0,
				"w_ms": 152000000.0, "rb": 41e11, "wb": 12e11},
			{"database_id": 5.0, "r": 920000000.0, "r_ms": 31400000000.0, "w": 41000000.0,
				"w_ms": 590000000.0, "rb": 92e11, "wb": 31e11},
			{"database_id": 6.0, "r": 143000000.0, "r_ms": 4110000000.0, "w": 8100000.0,
				"w_ms": 129000000.0, "rb": 18e11, "wb": 6e11},
			{"database_id": 7.0, "r": 61000000.0, "r_ms": 1450000000.0, "w": 3100000.0,
				"w_ms": 42000000.0, "rb": 8e11, "wb": 2e11},
		}, nil
	}
	if strings.Contains(U, "TEMPDB.SYS.DM_DB_FILE_SPACE_USAGE") {
		version := 320.0
		if inH(h, 14.9, 15.6) {
			version = 2600.0
		}
		return []map[string]any{{"user_mb": 2310.0, "internal_mb": 980.0,
			"version_mb": version, "free_mb": 4120.0}}, nil
	}

	// ---------- глубокая диагностика ----------
	if strings.Contains(U, "@@SERVERNAME") {
		return []map[string]any{d.verRow()}, nil
	}
	if strings.Contains(U, "DM_HADR_INSTANCE_REPLICA_STATES") {
		return []map[string]any{{"n": float64(0)}}, nil
	}
	if strings.Contains(U, "FROM SYS.CONFIGURATIONS") {
		cfgRow := func(name string, inUse, val, mn, mx float64, dyn, adv int) map[string]any {
			return map[string]any{"name": name, "value_in_use": inUse, "value": val,
				"minimum": mn, "maximum": mx,
				"is_dynamic": float64(dyn), "is_advanced": float64(adv)}
		}
		return []map[string]any{
			cfgRow("max server memory (MB)", 2147483647, 2147483647, 16, 2147483647, 1, 1),
			cfgRow("min server memory (MB)", 0, 0, 0, 2147483647, 1, 1),
			cfgRow("max degree of parallelism", 0, 0, 0, 32767, 1, 1),
			cfgRow("cost threshold for parallelism", 5, 5, 0, 32767, 1, 1),
			cfgRow("optimize for ad hoc workloads", 0, 0, 0, 1, 1, 1),
			cfgRow("max worker threads", 0, 0, 128, 65535, 1, 1),
			cfgRow("clr enabled", 0, 0, 0, 1, 1, 1),
			cfgRow("xp_cmdshell", 0, 0, 0, 1, 1, 1),
			cfgRow("priority boost", 0, 0, 0, 1, 0, 1),
			cfgRow("lightweight pooling", 0, 0, 0, 1, 0, 1),
			cfgRow("remote query timeout (s)", 600, 600, 0, 2147483647, 1, 0),
			cfgRow("blocked process threshold (s)", 0, 0, 0, 86400, 1, 1),
		}, nil
	}
	if strings.Contains(U, "DM_OS_SYS_INFO") {
		return []map[string]any{{"cpu_count": float64(16), "hyperthread_ratio": float64(2),
			"sqlserver_start_time": demoAgo(11, 3, 0),
			"phys_kb":              34359738368.0, "committed_kb": 25100000.0,
			"committed_target_kb": 25160000.0}}, nil
	}
	if strings.Contains(U, "DM_OS_SCHEDULERS") {
		return []map[string]any{{"total": float64(18), "online": float64(16),
			"nodes": float64(1)}}, nil
	}
	if strings.Contains(U, "DM_OS_PROCESS_MEMORY") {
		return []map[string]any{{"in_use_kb": 24980000.0, "vas_kb": 137438953472.0}}, nil
	}
	if strings.Contains(U, "DM_SERVER_SERVICES") {
		return []map[string]any{
			{"servicename": "SQL Server (SQL2022)", "startup_type_desc": "Automatic",
				"status_desc": "Running", "instant_file_initialization_enabled": "N", "service_account": `NT Service\MSSQL$SQL2022`},
			{"servicename": "SQL Server Agent (SQL2022)", "startup_type_desc": "Automatic",
				"status_desc": "Running", "service_account": `NT Service\SQLAgent$SQL2022`},
			{"servicename": "SQL Server Browser", "startup_type_desc": "Disabled",
				"status_desc": "Stopped", "service_account": `NT AUTHORITY\LOCAL SERVICE`},
		}, nil
	}
	if strings.Contains(U, "DM_OS_MEMORY_CLERKS") {
		return []map[string]any{
			{"type": "MEMORYCLERK_SQLBUFFERPOOL", "pages_kb": 22910000000.0 / 1024},
			{"type": "CACHESTORE_SQLCP", "pages_kb": 2620000.0},
			{"type": "CACHESTORE_OBJCP", "pages_kb": 640000.0},
			{"type": "MEMORYCLERK_SQLQUERYEXEC", "pages_kb": 410000.0},
			{"type": "OBJECTSTORE_LOCK_MANAGER", "pages_kb": 190000.0},
			{"type": "MEMORYCLERK_SQLCLR", "pages_kb": 60000.0},
			{"type": "CACHESTORE_PHDR", "pages_kb": 120000.0},
			{"type": "MEMORYCLERK_SQLGENERAL", "pages_kb": 105000.0},
		}, nil
	}
	if strings.Contains(U, "DM_EXEC_CACHED_PLANS") {
		return []map[string]any{
			{"objtype": "Adhoc", "cnt": 52300.0, "size_b": 3900000000.0},
			{"objtype": "Prepared", "cnt": 8100.0, "size_b": 610000000.0},
			{"objtype": "Proc", "cnt": 4200.0, "size_b": 480000000.0},
			{"objtype": "CheckClusive", "cnt": 120.0, "size_b": 9000000.0},
		}, nil
	}
	if strings.Contains(U, "DM_EXEC_QUERY_PLAN") {
		return []map[string]any{
			{"plan_xml": planXMLConv, "sql_text": demoSQLTexts[0]},
			{"plan_xml": planXMLSpill, "sql_text": demoSQLTexts[1]},
			{"plan_xml": planXMLOk, "sql_text": demoSQLTexts[3]},
		}, nil
	}
	if strings.Contains(U, "DM_EXEC_QUERY_STATS") {
		if strings.Contains(U, "QUERY_HASH") {
			return []map[string]any{
				{"query_hash": "0x7a11e2b4c9d0f1a2", "plan_count": 24.0, "execs": 51000.0,
					"sql_text": "SELECT * FROM dbo.Orders WHERE CustomerId = @p0 AND " +
						"Status IN (@p1, @p2)"},
				{"query_hash": "0x1f2e3d4c5b6a7988", "plan_count": 11.0, "execs": 12000.0,
					"sql_text": "SELECT TOP (@n) * FROM dbo.Events WHERE Kind = @k"},
			}, nil
		}
		order := "avg"
		if strings.Contains(U, "TOTAL_WORKER_TIME DESC") {
			order = "worker"
		} else if strings.Contains(U, "TOTAL_LOGICAL_READS DESC") {
			order = "reads"
		}
		base := d.qstatsRows()
		if order == "avg" {
			base = base[1:5]
		}
		return base, nil
	}
	if strings.Contains(U, "DM_DB_MISSING_INDEX_DETAILS") {
		return []map[string]any{
			{"obj": "[SalesDB].[dbo].[Orders]", "equality_columns": "[CustomerId], [Status]",
				"inequality_columns": nil, "included_columns": "[CreatedAt], [Number]",
				"unique_compiles": 340.0, "user_seeks": 48211.0, "user_scans": 0.0,
				"avg_user_impact": 91.3, "last_user_seek": demoAgo(0, 1, 0)},
			{"obj": "[SalesDB].[dbo].[Events]", "equality_columns": "[Kind]",
				"inequality_columns": "[CreatedAt]", "included_columns": "[Payload]",
				"unique_compiles": 120.0, "user_seeks": 19840.0, "user_scans": 0.0,
				"avg_user_impact": 74.8, "last_user_seek": demoAgo(0, 0, 30)},
			{"obj": "[SalesDB].[dbo].[Sessions]", "equality_columns": "[UserId]",
				"inequality_columns": "[ExpiresAt]", "included_columns": nil,
				"unique_compiles": 60.0, "user_seeks": 9021.0, "user_scans": 0.0,
				"avg_user_impact": 68.1, "last_user_seek": demoAgo(0, 2, 0)},
		}, nil
	}
	if strings.Contains(U, "DM_DB_INDEX_PHYSICAL_STATS") {
		return []map[string]any{
			{"sch": "dbo", "tbl": "Events", "ix_name": "IX_Events_CreatedAt",
				"frag": 71.4, "page_count": 128400.0, "fill_factor": 0.0,
				"type_desc": "NONCLUSTERED", "db": "SalesDB"},
			{"sch": "dbo", "tbl": "Orders", "ix_name": "IX_Orders_CustomerId",
				"frag": 44.9, "page_count": 52100.0, "fill_factor": 0.0,
				"type_desc": "NONCLUSTERED", "db": "SalesDB"},
			{"sch": "dbo", "tbl": "OrderLines", "ix_name": "PK_OrderLines",
				"frag": 33.2, "page_count": 96200.0, "fill_factor": 0.0,
				"type_desc": "CLUSTERED", "db": "SalesDB"},
			{"sch": "dbo", "tbl": "Sessions", "ix_name": "IX_Sessions_Expires",
				"frag": 21.8, "page_count": 2200.0, "fill_factor": 0.0,
				"type_desc": "NONCLUSTERED", "db": "SalesDB"},
		}, nil
	}
	if strings.Contains(U, "DM_DB_STATS_PROPERTIES") {
		return []map[string]any{
			{"sch": "dbo", "tbl": "Orders", "stat_name": "_WA_Sys_00000007_14E61C24",
				"last_updated": demoAgo(23, 0, 0), "rows": 4820000.0,
				"rows_sampled": 120000.0, "modification_counter": 2910000.0,
				"no_recompute": 0.0, "db": "SalesDB"},
			{"sch": "dbo", "tbl": "Events", "stat_name": "IX_Events_CreatedAt",
				"last_updated": demoAgo(16, 0, 0), "rows": 31100000.0,
				"rows_sampled": 770000.0, "modification_counter": 18400000.0,
				"no_recompute": 0.0, "db": "SalesDB"},
			{"sch": "dbo", "tbl": "Customers", "stat_name": "PK_Customers",
				"last_updated": demoAgo(2, 0, 0), "rows": 96000.0, "rows_sampled": 96000.0,
				"modification_counter": 900.0, "no_recompute": 0.0, "db": "SalesDB"},
		}, nil
	}
	if strings.Contains(U, "DM_DB_INDEX_USAGE_STATS") {
		return []map[string]any{
			{"sch": "dbo", "tbl": "Orders", "ix_name": "IX_Orders_Guid",
				"type_desc": "NONCLUSTERED", "reads": 0.0, "updates": 912000.0,
				"size_mb": 1024.0, "db": "SalesDB"},
			{"sch": "dbo", "tbl": "Events", "ix_name": "IX_Events_Old",
				"type_desc": "NONCLUSTERED", "reads": 0.0, "updates": 141000.0,
				"size_mb": 5120.0, "db": "SalesDB"},
			{"sch": "dbo", "tbl": "Sessions", "ix_name": "IX_Sessions_User",
				"type_desc": "NONCLUSTERED", "reads": 0.0, "updates": 42000.0,
				"size_mb": 640.0, "db": "SalesDB"},
		}, nil
	}
	if strings.Contains(U, "FROM SYS.DATABASES D") && strings.Contains(U, "STATE_DESC = N'ONLINE'") {
		dbRow := func(name, state, recovery, reuse string, compat int,
			verify string, rcsi, shrink, autoclose, snapshot int, isSys int) map[string]any {
			return map[string]any{"name": name, "state_desc": state,
				"recovery_model_desc": recovery, "log_reuse_wait_desc": reuse,
				"compatibility_level": float64(compat), "page_verify_option_desc": verify,
				"is_read_committed_snapshot_on":   float64(rcsi),
				"is_auto_shrink_on":               float64(shrink),
				"is_auto_close_on":                float64(autoclose),
				"is_auto_create_stats_on":         float64(1),
				"is_auto_update_stats_on":         float64(1),
				"target_recovery_time_in_seconds": float64(map[bool]int{true: 0, false: 60}[name == "SalesDB"]),
				"snapshot_isolation_state_desc":   map[int]string{1: "ON", 0: "OFF"}[snapshot],
				"log_reuse_wait":                  float64(map[string]int{"NOTHING": 0, "LOG_BACKUP": 2}[reuse]),
				"is_system":                       float64(isSys)}
		}
		return []map[string]any{
			dbRow("master", "ONLINE", "SIMPLE", "NOTHING", 160, "CHECKSUM", 1, 0, 0, 1, 1),
			dbRow("tempdb", "ONLINE", "SIMPLE", "NOTHING", 160, "CHECKSUM", 0, 0, 0, 1, 1),
			dbRow("SalesDB", "ONLINE", "FULL", "LOG_BACKUP", 110, "CHECKSUM", 0, 0, 0, 0, 0),
			dbRow("ReportDB", "ONLINE", "FULL", "LOG_BACKUP", 130, "TORN_PAGE_DETECTION", 0, 1, 0, 0, 0),
			dbRow("WarehouseDB", "ONLINE", "SIMPLE", "NOTHING", 150, "CHECKSUM", 1, 0, 0, 1, 0),
		}, nil
	}
	if strings.Contains(U, "FROM SYS.MASTER_FILES") && !strings.Contains(U, "DM_OS_VOLUME_STATS") {
		return []map[string]any{
			{"db": "master", "logical_name": "master", "type_desc": "ROWS",
				"size_mb": 6.0, "growth": 10.0, "is_percent_growth": 1.0,
				"physical_name": `C:\Program Files\...\master.mdf`},
			{"db": "SalesDB", "logical_name": "SalesDB_data", "type_desc": "ROWS",
				"size_mb": 61440.0, "growth": 1024.0, "is_percent_growth": 0.0,
				"physical_name": `E:\Data\SalesDB_data.mdf`},
			{"db": "SalesDB", "logical_name": "SalesDB_log", "type_desc": "LOG",
				"size_mb": 14336.0, "growth": 10.0, "is_percent_growth": 1.0,
				"physical_name": `F:\Logs\SalesDB_log.ldf`},
			{"db": "ReportDB", "logical_name": "ReportDB", "type_desc": "ROWS",
				"size_mb": 30720.0, "growth": 1.0, "is_percent_growth": 0.0,
				"physical_name": `E:\Data\ReportDB.mdf`},
			{"db": "ReportDB", "logical_name": "ReportDB_log", "type_desc": "LOG",
				"size_mb": 8192.0, "growth": 10.0, "is_percent_growth": 1.0,
				"physical_name": `E:\Data\ReportDB_log.ldf`},
			{"db": "WarehouseDB", "logical_name": "WH", "type_desc": "ROWS",
				"size_mb": 102400.0, "growth": 512.0, "is_percent_growth": 0.0,
				"physical_name": `G:\WH\WH.mdf`},
			{"db": "tempdb", "logical_name": "tempdev", "type_desc": "ROWS",
				"size_mb": 8192.0, "growth": 128.0, "is_percent_growth": 0.0,
				"physical_name": `D:\TempDB\tempdev.mdf`},
			{"db": "tempdb", "logical_name": "templog", "type_desc": "LOG",
				"size_mb": 2048.0, "growth": 64.0, "is_percent_growth": 0.0,
				"physical_name": `D:\TempDB\templog.ldf`},
		}, nil
	}
	if strings.Contains(U, "DM_DB_LOG_SPACE_USAGE") {
		used := map[string]float64{"SalesDB": 96.4, "ReportDB": 88.1, "WarehouseDB": 22.0}
		name := dbNameInSQL(s)
		pct := 30.0
		if v, ok := used[name]; ok {
			pct = v
		}
		return []map[string]any{{"log_mb": 14336.0, "used_pct": pct}}, nil
	}
	if strings.Contains(U, "DM_DB_LOG_INFO") {
		vlf := map[string]float64{"SalesDB": 1420, "ReportDB": 810, "WarehouseDB": 120}
		name := dbNameInSQL(s)
		if name == "" {
			return []map[string]any{}, nil
		}
		v := 90.0
		if x, ok := vlf[name]; ok {
			v = x
		}
		return []map[string]any{{"vlf": v}}, nil
	}
	if strings.Contains(U, "DM_OS_VOLUME_STATS") && strings.Contains(U, "DB_NAME(MF.DATABASE_ID)") {
		return []map[string]any{
			{"db": "SalesDB", "type_desc": "ROWS", "volume_mount_point": `E:\`},
			{"db": "SalesDB", "type_desc": "LOG", "volume_mount_point": `F:\`},
			{"db": "ReportDB", "type_desc": "ROWS", "volume_mount_point": `D:\`},
			{"db": "tempdb", "type_desc": "ROWS", "volume_mount_point": `D:\`},
		}, nil
	}
	if strings.Contains(U, "DM_OS_VOLUME_STATS") {
		return []map[string]any{
			{"volume_mount_point": `C:\`, "file_system_type": "NTFS",
				"total_gb": 119.87, "free_gb": 41.22},
			{"volume_mount_point": `E:\`, "file_system_type": "NTFS",
				"total_gb": 1023.9, "free_gb": 68.4},
			{"volume_mount_point": `F:\`, "file_system_type": "NTFS",
				"total_gb": 511.9, "free_gb": 38.7},
			{"volume_mount_point": `G:\`, "file_system_type": "NTFS",
				"total_gb": 2047.9, "free_gb": 912.0},
			{"volume_mount_point": `D:\`, "file_system_type": "NTFS",
				"total_gb": 255.9, "free_gb": 133.2},
		}, nil
	}
	if strings.Contains(U, "TEMPDB.SYS.DATABASE_FILES") {
		return []map[string]any{
			{"name": "tempdev", "size_mb": 8192.0,
				"physical_name": `D:\TempDB\tempdev.mdf`, "type_desc": "ROWS", "growth": 8192.0, "is_percent_growth": 0.0},
			{"name": "templog", "size_mb": 2048.0,
				"physical_name": `D:\TempDB\templog.ldf`, "type_desc": "LOG"},
		}, nil
	}
	if strings.Contains(U, "DM_TRAN_ACTIVE_SNAPSHOT_DATABASE_TRANSACTIONS") {
		return []map[string]any{{"session_id": 61.0, "elapsed_time_seconds": 640.0,
			"db": "SalesDB"}}, nil
	}
	if strings.Contains(U, "DATABASE_QUERY_STORE_OPTIONS") {
		name := dbNameInSQL(s)
		if name == "" {
			return []map[string]any{}, nil
		}
		state := "READ_WRITE"
		if name == "SalesDB" || name == "ReportDB" {
			state = "OFF"
		}
		return []map[string]any{{"state": state}}, nil
	}
	if strings.Contains(U, "MSDB.DBO.BACKUPSET") {
		return []map[string]any{
			{"db": "SalesDB", "recovery_model_desc": "FULL",
				"last_full": demoAgo(0, 20, 0), "last_diff": demoAgo(0, 11, 0),
				"last_log": demoAgo(0, 0, 43)},
			{"db": "ReportDB", "recovery_model_desc": "FULL",
				"last_full": demoAgo(10, 2, 0), "last_diff": nil, "last_log": nil},
			{"db": "WarehouseDB", "recovery_model_desc": "SIMPLE",
				"last_full": demoAgo(1, 6, 0), "last_diff": demoAgo(1, 2, 0), "last_log": nil},
		}, nil
	}
	if strings.Contains(U, "SYSJOBHISTORY") {
		return []map[string]any{
			{"job": "Ночное обслуживание индексов", "step_id": 1.0, "step_name": "Rebuild",
				"run_date": float64(mustAtoi(time.Now().Format("20060102"))),
				"run_time": 21200.0, "run_status": 0.0,
				"message": "Executed as user: sa. ... The job step was retried without " +
					"success. Consequently, the step failed. [SQLSTATE 42000] " +
					"(Error 3147) Backup and file manipulation operations ... " +
					"must be serialized."},
			{"job": "Импорт из 1С", "step_id": 2.0, "step_name": "Load",
				"run_date": float64(mustAtoi(time.Now().Format("20060102"))),
				"run_time": 41500.0, "run_status": 0.0,
				"message": "Executed as user: sa. ... Ошибка: Время ожидания операции " +
					"истекло. [SQLSTATE HY000] (Error -2). Шаг завершился с ошибкой."},
		}, nil
	}
	if strings.Contains(U, "SYSTEM_HEALTH") {
		return []map[string]any{{"td": deadlockXML}}, nil
	}
	if strings.Contains(U, "SP_READERRORLOG") {
		return []map[string]any{
			{"logdate": demoAgo(2, 5, 0), "processinfo": "spid61",
				"text": "SQL Server has encountered 184 occurrence(s) of I/O requests " +
					"taking longer than 15 seconds to complete on file " +
					"[E:\\Data\\ReportDB.mdf] in database [ReportDB] (8)."},
			{"logdate": demoAgo(1, 9, 0), "processinfo": "spid55",
				"text": "autogrow of database file took 438 milliseconds. Consider using " +
					"ALTER DATABASE ... FILEGROWTH..."},
			{"logdate": demoAgo(0, 12, 0), "processinfo": "spid63",
				"text": "autogrow of database file took 1290 milliseconds."},
			{"logdate": demoAgo(0, 3, 0), "processinfo": "spid70",
				"text": "Error: 17883, Severity: 1, State: 0. The Scheduler 7 appears to " +
					"be hung. SPID 0x1a2b..."},
			{"logdate": demoAgo(0, 7, 0), "processinfo": "Backup",
				"text": "Database backed up. Database: master..."},
		}, nil
	}
	if strings.Contains(U, "TRACESTATUS") {
		return []map[string]any{{"TraceFlag": 3226.0, "Status": 1.0, "Global": 1.0, "Session": 1.0},
			{"TraceFlag": 1118.0, "Status": 1.0, "Global": 1.0, "Session": 1.0}}, nil
	}
	if strings.Contains(U, "MOST_RECENT_SQL_HANDLE") {
		return []map[string]any{{"session_id": 58.0, "login_name": demoLogin,
			"host_name": demoHost, "program_name": demoProg, "status": "sleeping",
			"db_name": "SalesDB", "tran_count": 3.0,
			"sql_text": "BEGIN TRAN UPDATE TOP (500) o SET o.Processed = 1, " +
				"o.UpdatedBy = SUSER_SNAME() FROM dbo.Orders o WHERE " +
				"o.Processed = 0 -- далее курсор и PRINT в цикле",
			"wait_type": nil, "wait_time": nil}}, nil
	}
	return nil, fmt.Errorf("демо: неизвестный запрос: %s", strHead(s, 120))
}

func mustAtoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// dbNameInSQL — извлечь [ИмяБазы] из SQL вида [DB].sys.…
func dbNameInSQL(s string) string {
	i := strings.Index(s, "[")
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], "]")
	if j < 0 {
		return ""
	}
	return s[i+1 : i+j]
}

func (d *DemoDatabase) requests(h float64) []map[string]any {
	rows := []map[string]any{{
		"session_id": 58.0, "status": "running", "blocking_session_id": 0.0,
		"wait_type": nil, "wait_time": 0.0, "wait_resource": nil, "cpu_time": 145000.0,
		"total_elapsed_time": 210000.0, "reads": 84200.0, "writes": 3100.0,
		"logical_reads": 2412000.0, "command": "UPDATE", "percent_complete": 0.0,
		"db_name": "SalesDB", "login_name": demoLogin, "host_name": demoHost,
		"program_name": demoProg, "session_status": "running",
		"sql_text": demoSQLTexts[2], "object_name": nil,
	}}
	if demoLoad(h) > 0.3 {
		rows = append(rows,
			map[string]any{"session_id": 61.0, "status": "suspended",
				"blocking_session_id": 58.0, "wait_type": "LCK_M_IX", "wait_time": 92000.0,
				"wait_resource": "5:1:92831", "cpu_time": 30.0, "total_elapsed_time": 95000.0,
				"reads": 0.0, "writes": 0.0, "logical_reads": 102.0, "command": "UPDATE",
				"percent_complete": 0.0, "db_name": "SalesDB", "login_name": `CORP\app_user`,
				"host_name": "APP-SRV-02", "program_name": ".Net SqlClient Data Provider",
				"session_status": "sleeping", "sql_text": demoSQLTexts[2], "object_name": nil},
			map[string]any{"session_id": 62.0, "status": "suspended",
				"blocking_session_id": 58.0, "wait_type": "LCK_M_S", "wait_time": 71000.0,
				"wait_resource": "5:2:118322", "cpu_time": 12.0, "total_elapsed_time": 73000.0,
				"reads": 210.0, "writes": 0.0, "logical_reads": 9400.0, "command": "SELECT",
				"percent_complete": 0.0, "db_name": "SalesDB", "login_name": `CORP\report_user`,
				"host_name": "BI-SRV", "program_name": "Report Builder",
				"session_status": "sleeping", "sql_text": demoSQLTexts[0], "object_name": nil},
			map[string]any{"session_id": 77.0, "status": "running",
				"blocking_session_id": 0.0, "wait_type": "PAGEIOLATCH_SH", "wait_time": 340.0,
				"wait_resource": "5:3:11945", "cpu_time": 890000.0, "total_elapsed_time": 940000.0,
				"reads": 4100000.0, "writes": 0.0, "logical_reads": 98200000.0, "command": "SELECT",
				"percent_complete": 0.0, "db_name": "SalesDB", "login_name": `CORP\bi`,
				"host_name": "BI-SRV", "program_name": "Power BI",
				"session_status": "running", "sql_text": demoSQLTexts[1], "object_name": nil},
		)
	}
	return rows
}

func (d *DemoDatabase) qstatsRows() []map[string]any {
	row := func(exec, totEl, totCPU, totReads float64, text string, obj any, db string) map[string]any {
		var objName any
		if s, ok := obj.(string); ok {
			objName = s
		}
		return map[string]any{"execution_count": exec, "total_worker_time": totCPU,
			"total_elapsed_time": totEl, "total_logical_reads": totReads,
			"total_physical_reads": float64(int64(totReads) / 20),
			"total_logical_writes": float64(int64(totReads) / 400),
			"total_spills":         1800.0,
			"creation_time":        demoAgo(10, 0, 0),
			"last_execution_time":  demoAgo(0, 0, 3),
			"db_name":              db,
			"schema_name":          "dbo",
			"object_name":          objName,
			"sql_text":             text}
	}
	return []map[string]any{
		row(1840, 412000000, 388000000, 51200000000, demoSQLTexts[1], "usp_CalcDailyReport", "SalesDB"),
		row(91000, 296000000, 281000000, 94000000000, demoSQLTexts[0], nil, "SalesDB"),
		row(220, 180000000, 174000000, 31000000000,
			"SELECT c.Name, COUNT(*) FROM dbo.Orders o JOIN dbo.Customers c ... "+
				"GROUP BY c.Name ORDER BY 2 DESC", nil, "SalesDB"),
		row(610000, 91000000, 88000000, 14000000000, demoSQLTexts[3], nil, "ReportDB"),
		row(4400, 64000000, 61000000, 8800000000, demoSQLTexts[4], nil, "SalesDB"),
		row(980000, 42000000, 41000000, 2100000000, demoSQLTexts[5], nil, "SalesDB"),
	}
}

const planXMLConv = `<?xml version="1.0" encoding="utf-16"?>
<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.481" Build="16.0.4165.4">
<BatchSequence><Batch><Statements><StmtSimple StatementText="SELECT o.Id, o.Number, c.Name FROM dbo.Orders o JOIN dbo.Customers c ON c.Id = o.CustomerId WHERE o.CreatedAt &gt;= @1 AND o.Status = @2" StatementId="1">
<QueryPlan DegreeOfParallelism="4" CachedPlanSize="128">
<Warnings><PlanAffectingConvert ConvertIssue="Seek Plan" Expression="[dbo].[Orders].[Status] = Convert(nvarchar(10),[@2],0)"/></Warnings>
<RelOp AvgRowSize="45" EstimateRows="1892.4" LogicalOp="Clustered Index Scan" PhysicalOp="Clustered Index Scan">
<IndexScan Ordered="0" ForcedIndex="0" NoExpandHint="0"><Object Database="[SalesDB]" Schema="[dbo]" Table="[Orders]" Index="[PK_Orders]"/></IndexScan>
</RelOp></QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

const planXMLSpill = `<?xml version="1.0" encoding="utf-16"?>
<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.481" Build="16.0.4165.4">
<BatchSequence><Batch><Statements><StmtSimple StatementText="exec dbo.usp_CalcDailyReport @Date = '2026-09-30', @Region = 4" StatementId="1">
<QueryPlan DegreeOfParallelism="8" CachedPlanSize="512">
<Warnings><SpillToTempDb SpillLevel="2" SpilledThreadCount="8" EstRows="2100000"/></Warnings>
<RelOp LogicalOp="Sort" PhysicalOp="Sort"><Object Database="[SalesDB]" Schema="[dbo]" Table="[Events]"/></RelOp>
</QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

const planXMLOk = `<?xml version="1.0" encoding="utf-16"?>
<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.481" Build="16.0.4165.4">
<BatchSequence><Batch><Statements><StmtSimple StatementText="INSERT INTO dbo.ReportCache..." StatementId="1">
<QueryPlan DegreeOfParallelism="1" CachedPlanSize="24">
<RelOp LogicalOp="Clustered Index Insert" PhysicalOp="Clustered Index Insert"/>
</QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

const deadlockXML = `<RingBufferTarget truncated="0" processingTime="2459023" totalEventsProcessed="14812">
<Event name="xml_deadlock_report" package="sqlserver" timestamp="2026-10-01T15:03:12.100Z">
<data name="xml_report"><value>
<deadlock><victim-list><victimProcess id="process1850a4c8108"/></victim-list>
<process-list>
<process id="process1850a4c8108" taskpriority="0" logused="0" waitresource="KEY: 5:72057594047512576 (8194443284a0)" waittime="7335" ownerId="9182736" transactionname="SELECT" lasttranstarted="2026-10-01T15:03:04.770" XDES="0x1850b130848" lockMode="S" schedulerid="9" kpid="0" status="suspended" spid="62" sbid="0" ecid="0" priority="0" trancount="0" lastbatchstarted="2026-10-01T15:03:04.770" lastbatchcompleted="2026-10-01T15:03:04.750" clientapp="Report Builder" hostname="BI-SRV" hostpid="4212" loginname="CORP\report_user" isolationlevel="read committed (2)" currentdatabase="SalesDB" currentoptimizer="2">
<inputbuf>SELECT o.Id, o.Number, c.Name FROM dbo.Orders o JOIN dbo.Customers c ON c.Id = o.CustomerId WHERE o.CreatedAt &gt;= '2026-09-30' AND o.Status = 'NEW'</inputbuf></process>
<process id="process1850a4c9230" taskpriority="0" logused="20480" waitresource="KEY: 5:72057594046857216 (61a06abd401c)" waittime="7340" ownerId="9182735" transactionname="user_transaction" lasttranstarted="2026-10-01T15:02:57.590" XDES="0x1850b0a9088" lockMode="X" schedulerid="11" kpid="0" status="suspended" spid="58" sbid="0" ecid="0" priority="0" trancount="2" lastbatchstarted="2026-10-01T15:03:04.750" lastbatchcompleted="2026-10-01T15:03:04.700" clientapp=".Net SqlClient Data Provider" hostname="APP-SRV-07" loginname="CORP\srv-report" isolationlevel="read committed (2)" currentdatabase="SalesDB">
<inputbuf>UPDATE TOP (500) o SET o.Processed = 1, o.UpdatedBy = SUSER_SNAME() FROM dbo.Orders o WHERE o.Processed = 0</inputbuf></process>
</process-list>
<resource-list>
<keylock hobtid="72057594047512576" dbid="5" objectname="SalesDB.dbo.Orders" indexname="PK_Orders" id="lock18509c2b300" mode="X" associatedObjectId="72057594047512576"/>
<keylock hobtid="72057594046857216" dbid="5" objectname="SalesDB.dbo.Orders" indexname="IX_Orders_CustomerId" id="lock18509c2b580" mode="S" associatedObjectId="72057594046857216"/>
</resource-list></deadlock>
</value></data></Event>
<Event name="xml_deadlock_report" package="sqlserver" timestamp="2026-09-30T11:22:44.310Z">
<data name="xml_report"><value>
<deadlock><victim-list><victimProcess id="processaaa"/></victim-list>
<process-list>
<process id="processaaa" spid="71" waitresource="KEY: 7:72057594051555328 (8fa35)" waittime="4122" transactionname="SELECT" lastbatchstarted="2026-09-30T11:22:40.190" clientapp="Power BI" hostname="BI-SRV" loginname="CORP\bi" isolationlevel="read committed (2)" currentdatabase="ReportDB">
<inputbuf>SELECT * FROM dbo.FactSales WHERE DateKey = 20260929 AND RegionKey = 4</inputbuf></process>
<process id="processbbb" spid="69" waitresource="KEY: 7:72057594051555329 (8fa99)" waittime="4125" transactionname="user_transaction" lastbatchstarted="2026-09-30T11:22:39.990" clientapp="SQLAgent - Ticker" hostname="DEMO-SQL" loginname="sa" isolationlevel="read committed (2)" currentdatabase="ReportDB">
<inputbuf>UPDATE dbo.FactSales SET Amount = Amount * 1.0 WHERE DateKey = 20260929</inputbuf></process>
</process-list>
<resource-list><keylock hobtid="72057594051555328" dbid="7" objectname="ReportDB.dbo.FactSales" indexname="PK_FactSales" id="lock1" mode="X" associatedObjectId="72057594051555328"/></resource-list></deadlock>
</value></data></Event>
</RingBufferTarget>`

// =====================================================================
// ДЕМО-ТАЙМЛАЙН СУТОЧНОГО МОНИТОРИНГА (24 ч, 1 минута)
// =====================================================================

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

func generateDemoSamples(intervalSec, hours int) []Sample {
	rnd := rand.New(rand.NewSource(42))
	now := time.Now().Unix()
	n := hours * 3600 / intervalSec
	samples := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		ts := now - int64((n-1-i)*intervalSec)
		lt := time.Unix(ts, 0)
		h := float64(lt.Hour()) + float64(lt.Minute())/60.0
		ld := demoLoad(h)
		peak := inH(h, 9.6, 10.9)
		block := inH(h, 15.0, 15.6)
		cpu := demoCPU(h) + rnd.Float64()*6 - 3
		batch := demoBatch(h) * (0.85 + rnd.Float64()*0.3)
		if block {
			batch *= 0.8
		}
		var ple int
		switch {
		case peak:
			ple = 265 + rnd.Intn(51) - 25
		case ld > 0.35:
			ple = 400 + rnd.Intn(121) - 60
		default:
			ple = 1500 + rnd.Intn(501) - 250
		}
		bchr := 97.5 + rnd.Float64() - 0.5
		if peak {
			bchr = 94.3 + rnd.Float64()*1.2 - 0.6
		}
		var lazy float64
		switch {
		case peak:
			lazy = 35 + rnd.Float64()*16 - 8
		case ld > 0.35:
			lazy = 5
		default:
			lazy = 1.0
		}
		ioRd := 9 + 4*ld + rnd.Float64()*3 - 1
		if peak {
			ioRd = 38 + rnd.Float64()*12 - 6
		}
		ioWr := 6 + 3*ld + rnd.Float64()*2 - 1

		type wt struct {
			w  string
			ms float64
		}
		var waits []wt
		switch {
		case peak:
			waits = []wt{{"PAGEIOLATCH_SH", 28 + rnd.Float64()*8},
				{"CXPACKET", 12 + rnd.Float64()*6},
				{"CXCONSUMER", 5 + rnd.Float64()*4},
				{"SOS_SCHEDULER_YIELD", 6 + rnd.Float64()*4},
				{"RESOURCE_SEMAPHORE", 2 + rnd.Float64()*2}}
		case block:
			waits = []wt{{"LCK_M_IX", 30 + rnd.Float64()*12},
				{"LCK_M_S", 15 + rnd.Float64()*9},
				{"PAGEIOLATCH_SH", 4 + rnd.Float64()*4},
				{"WRITELOG", 3 + rnd.Float64()*3}}
		case ld > 0.35:
			waits = []wt{{"PAGEIOLATCH_SH", 8 + rnd.Float64()*6},
				{"CXPACKET", 4 + rnd.Float64()*4},
				{"ASYNC_NETWORK_IO", 3 + rnd.Float64()*4},
				{"WRITELOG", 3 + rnd.Float64()*3},
				{"SOS_SCHEDULER_YIELD", 2 + rnd.Float64()*3}}
		default:
			waits = []wt{{"WRITELOG", 1 + rnd.Float64()*2},
				{"ASYNC_NETWORK_IO", 1 + rnd.Float64()},
				{"BACKUPIO", rnd.Float64() * 2}}
		}
		wd := make([]WaitDelta, 0, len(waits))
		for _, w := range waits {
			wd = append(wd, WaitDelta{Type: w.w,
				WaitMS:   mathRound3(w.ms * float64(intervalSec) / 60.0),
				SignalMS: mathRound3(w.ms * 0.2),
				Tasks:    mathRound1(w.ms / 8.0)})
		}
		blocked := 0
		maxWait := 0.0
		if block {
			blocked, maxWait = 6, 138.0
		}
		samples = append(samples, Sample{
			TS: ts, N: i + 1,
			SQLCPU:          fp(round1(maxf(0, cpu))),
			SysCPU:          fp(round1(maxf(0, cpu*1.12+4))),
			BatchReq:        fp(round1(batch)),
			TransSec:        fp(round1(batch * 3.1)),
			CompilesSec:     fp(round1(batch * tf(peak, 0.2, 0.08))),
			RecompilesSec:   fp(round2(batch * 0.01)),
			LazyWrites:      fp(round1(lazy)),
			PLE:             ip(ple),
			BCHR:            fp(round2(bchr)),
			FullScans:       fp(round1(20 + 260*ld)),
			IndexSearches:   fp(round1(2800 + 20000*ld)),
			MemTargetMB:     fp(24576.0),
			MemTotalMB:      fp(round1(23900 + 300*ld)),
			IORdLat:         fp(round2(ioRd)),
			IOWrLat:         fp(round2(ioWr)),
			IORdMBps:        fp(round1(40 + 240*ld)),
			IOWrMBps:        fp(round1(12 + 80*ld)),
			TempDBUsedMB:    fp(round1(2600 + 1800*ld + tb(block, 4200))),
			TempDBVersionMB: fp(round1(tb(block, 2400, 300*ld))),
			Sessions:        ip(int(36 + 40*ld)),
			Blocked:         blocked,
			BlockedMaxWait:  maxWait,
			Waits:           wd,
		})
	}
	return samples
}

func tf(b bool, t, f float64) float64 {
	if b {
		return t
	}
	return f
}

func tb(b bool, t float64, f ...float64) float64 {
	if b {
		return t
	}
	if len(f) > 0 {
		return f[0]
	}
	return 0
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func round1(v float64) float64     { return float64(int64(v*10+0.5*sgn(v))) / 10 }
func round2(v float64) float64     { return float64(int64(v*100+0.5*sgn(v))) / 100 }
func round3(v float64) float64     { return float64(int64(v*1000+0.5*sgn(v))) / 1000 }
func mathRound3(v float64) float64 { return round3(v) }

func sgn(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

func generateDemoEvents(samples []Sample) (blocking []BlockingEvent, longq []LongQuery) {
	for i := range samples {
		s := &samples[i]
		lt := time.Unix(s.TS, 0)
		h := float64(lt.Hour()) + float64(lt.Minute())/60.0
		if inH(h, 15.0, 15.55) && s.TS%180 < 60 {
			type be struct {
				sid, bsid           int
				wt, wres, cmd, text string
			}
			for _, e := range []be{
				{61, 58, "LCK_M_IX", "5:1:92831", "UPDATE", demoSQLTexts[2]},
				{62, 58, "LCK_M_S", "5:2:118322", "SELECT", demoSQLTexts[0]},
				{63, 61, "LCK_M_IX", "5:1:92834", "UPDATE", demoSQLTexts[2]},
			} {
				blocking = append(blocking, BlockingEvent{
					TS: s.TS, SessionID: e.sid, BlockingSessionID: e.bsid,
					WaitType: e.wt, WaitTimeS: float64(90 + int(s.TS%60)),
					WaitResource: e.wres, Command: e.cmd, Login: demoLogin,
					Host: demoHost, Program: demoProg, DB: "SalesDB",
					ElapsedS: float64(120 + int(s.TS%90)),
					TextHead: strHead(e.text, 400)})
			}
		}
		if inH(h, 9.55, 10.95) && lt.Minute()%5 == 0 {
			longq = append(longq, LongQuery{
				TS: s.TS, SessionID: 77, Login: `CORP\bi`, Program: "Power BI",
				DB: "SalesDB", ElapsedS: float64(620 + lt.Minute()*2), CPUS: 590,
				LogicalReads: 98200000, Writes: 0, Command: "SELECT",
				TextHead: demoSQLTexts[1], WaitType: "PAGEIOLATCH_SH"})
		}
		if inH(h, 15.0, 15.55) && lt.Minute()%4 == 0 {
			longq = append(longq, LongQuery{
				TS: s.TS, SessionID: 62, Login: `CORP\report_user`, Program: "Report Builder",
				DB: "SalesDB", ElapsedS: float64(140 + lt.Minute()), CPUS: 1,
				LogicalReads: 9400, Writes: 0, Command: "SELECT",
				TextHead: demoSQLTexts[0], WaitType: "LCK_M_S"})
		}
	}
	return blocking, longq
}
