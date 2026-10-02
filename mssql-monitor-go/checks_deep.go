// checks_deep.go — глубокая диагностика для отчёта (все проверки через DMV, только чтение).
package main

import (
	"encoding/xml"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

func collectDeep(db Querier, opts CheckOptions, progress func(string),
	blocking []BlockingEvent, longq []LongQuery) *DeepData {
	d := &DeepData{Errors: map[string]string{}, BlockingEvents: blocking, LongQueries: longq}

	safe := func(name string, fn func() error) {
		if progress != nil {
			progress(name)
		}
		if err := fn(); err != nil {
			d.Errors[name] = strHead(err.Error(), 300)
		}
	}

	safe("server", func() error { d.Server = serverInfo(db); return nil })
	safe("configs", func() error {
		rows, err := db.Query(
			"SELECT name, CAST(value_in_use AS NVARCHAR(64)) AS value_in_use, " +
				"CAST(value AS NVARCHAR(64)) AS value, " +
				"CAST(minimum AS NVARCHAR(64)) AS minimum, " +
				"CAST(maximum AS NVARCHAR(64)) AS maximum, is_dynamic, is_advanced " +
				"FROM sys.configurations WHERE name IN (" +
				"N'max server memory (MB)', N'min server memory (MB)', " +
				"N'max degree of parallelism', N'cost threshold for parallelism', " +
				"N'optimize for ad hoc workloads', N'max worker threads', " +
				"N'clr enabled', N'xp_cmdshell', N'priority boost', N'lightweight pooling', " +
				"N'remote query timeout (s)', N'blocked process threshold (s)')")
		d.Configs = rows
		return err
	})
	safe("sys_info", func() error { d.SysInfo = sysInfo(db); return nil })
	safe("clerks", func() error {
		rows, err := queryAny(db, []string{
			"SELECT TOP 15 type, SUM(CAST(pages_kb AS BIGINT)) AS pages_kb " +
				"FROM sys.dm_os_memory_clerks GROUP BY type ORDER BY 2 DESC",
			"SELECT TOP 15 type, SUM(CAST(single_pages_kb AS BIGINT)+" +
				"CAST(multi_page_kb AS BIGINT)) AS pages_kb " +
				"FROM sys.dm_os_memory_clerks GROUP BY type ORDER BY 2 DESC"})
		d.Clerks = rows
		return err
	})
	safe("plan_cache", func() error {
		rows, err := db.Query(
			"SELECT objtype, COUNT(*) AS cnt, SUM(CAST(size_in_bytes AS BIGINT)) AS size_b " +
				"FROM sys.dm_exec_cached_plans GROUP BY objtype ORDER BY 3 DESC")
		d.PlanCache = rows
		return err
	})
	safe("wait_stats", func() error { d.WaitStats = waitStats(db); return nil })
	safe("requests_live", func() error { d.Live = requestsLive(db); return nil })
	safe("qstats", func() error { d.QStats = qstats(db); return nil })
	safe("plan_issues", func() error {
		if !opts.PlanXML {
			return nil
		}
		d.PlanIssues = planIssues(db)
		return nil
	})
	safe("fragmentation", func() error {
		if !opts.Fragmentation {
			return nil
		}
		d.Frag = fragmentation(db)
		return nil
	})
	safe("missing_indexes", func() error { d.MissingIdx = missingIndexes(db); return nil })
	safe("stats", func() error { d.Stats = statsStale(db); return nil })
	safe("unused_indexes", func() error { d.UnusedIdx = unusedIndexes(db); return nil })
	safe("databases", func() error {
		var err error
		d.DBs, err = databases(db)
		return err
	})
	safe("io_files", func() error {
		rows, err := db.Query(
			"SELECT DB_NAME(vfs.database_id) AS db, mf.name AS logical_name, " +
				"mf.type_desc, vfs.num_of_reads, vfs.io_stall_read_ms, vfs.num_of_writes, " +
				"vfs.io_stall_write_ms, vfs.size_on_disk_bytes, mf.physical_name " +
				"FROM sys.dm_io_virtual_file_stats(NULL, NULL) vfs " +
				"JOIN sys.master_files mf ON mf.database_id = vfs.database_id " +
				"AND mf.file_id = vfs.file_id")
		d.IOFiles = rows
		return err
	})
	safe("volumes", func() error {
		rows, err := db.Query(
			"SELECT DISTINCT vs.volume_mount_point, vs.file_system_type, " +
				"CAST(vs.total_bytes/1073741824.0 AS FLOAT) AS total_gb, " +
				"CAST(vs.available_bytes/1073741824.0 AS FLOAT) AS free_gb " +
				"FROM sys.master_files mf " +
				"CROSS APPLY sys.dm_os_volume_stats(mf.database_id, mf.file_id) vs")
		d.Volumes = rows
		return err
	})
	safe("tempdb", func() error { d.Tempdb = tempdbInfo(db); return nil })
	safe("trace_flags", func() error {
		rows, err := db.Query("DBCC TRACESTATUS(-1)")
		d.TraceFlags = rows
		return err
	})
	safe("query_store", func() error { d.QueryStore = queryStore(db); return nil })
	safe("backups", func() error {
		var err error
		d.Backups, err = backups(db)
		return err
	})
	safe("jobs_failed", func() error { d.JobsFailed = jobsFailed(db); return nil })
	safe("deadlocks", func() error { d.Deadlocks = deadlocks(db); return nil })
	safe("errorlog", func() error {
		if !opts.ErrorLog {
			return nil
		}
		rows, err := errorLog(db)
		d.ErrorLog = rows
		return err
	})
	return d
}

// ---------------- отдельные проверки ----------------

func serverInfo(db Querier) map[string]any {
	out := map[string]any{}
	row := queryRow(db, "SELECT @@SERVERNAME AS srv, @@VERSION AS v, "+
		"CAST(SERVERPROPERTY(N'InstanceName') AS NVARCHAR(128)) AS inst, "+
		"CAST(SERVERPROPERTY(N'Edition') AS NVARCHAR(128)) AS edition, "+
		"CAST(SERVERPROPERTY(N'ProductVersion') AS NVARCHAR(64)) AS ver, "+
		"CAST(SERVERPROPERTY(N'ProductLevel') AS NVARCHAR(32)) AS level, "+
		"CAST(SERVERPROPERTY(N'ProductMajorVersion') AS NVARCHAR(16)) AS major, "+
		"CAST(SERVERPROPERTY(N'ProductUpdateLevel') AS NVARCHAR(32)) AS cu, "+
		"CAST(SERVERPROPERTY(N'MachineName') AS NVARCHAR(128)) AS machine, "+
		"CAST(SERVERPROPERTY(N'IsClustered') AS NVARCHAR(4)) AS clustered, "+
		"CAST(SERVERPROPERTY(N'Collation') AS NVARCHAR(128)) AS collation")
	if row != nil {
		out = row
	}
	v := gs(out, "v")
	line := strings.SplitN(v, "\n", 2)[0]
	out["product_line"] = regexp.MustCompile(`\s*\(Build[^)]*\)\s*`).
		ReplaceAllString(line, " ")
	if m := regexp.MustCompile(`on (Windows NT [\d.]+|Linux)`).FindStringSubmatch(v); m != nil {
		out["os_line"] = m[1]
	}
	n := 0
	if row := queryRow(db, "SELECT COUNT(*) AS n FROM sys.dm_hadr_instance_replica_states"); row != nil {
		n = gii(row, "n")
	}
	out["ag_count"] = n
	return out
}

func sysInfo(db Querier) map[string]any {
	out := map[string]any{}
	rows, err := queryAny(db, []string{
		"SELECT cpu_count, hyperthread_ratio, sqlserver_start_time, " +
			"CAST(physical_memory_kb AS BIGINT) AS phys_kb, " +
			"CAST(committed_memory_kb AS BIGINT) AS committed_kb, " +
			"CAST(committed_target_memory_kb AS BIGINT) AS committed_target_kb " +
			"FROM sys.dm_os_sys_info",
		"SELECT cpu_count, hyperthread_ratio, sqlserver_start_time, " +
			"CAST(physical_memory_in_bytes AS BIGINT)/1024 AS phys_kb, " +
			"CAST(committed_memory_kb AS BIGINT) AS committed_kb, " +
			"CAST(committed_target_memory_kb AS BIGINT) AS committed_target_kb " +
			"FROM sys.dm_os_sys_info"})
	if err == nil && len(rows) > 0 {
		for k, v := range rows[0] {
			out[k] = v
		}
	}
	if row := queryRow(db, "SELECT COUNT(*) AS total, "+
		"SUM(CASE WHEN status = N'VISIBLE ONLINE' THEN 1 ELSE 0 END) AS online, "+
		"COUNT(DISTINCT parent_node_id) AS nodes FROM sys.dm_os_schedulers"); row != nil {
		for k, v := range row {
			out[k] = v
		}
	}
	if row := queryRow(db, "SELECT CAST(physical_memory_in_use_kb AS BIGINT) AS in_use_kb, "+
		"CAST(total_virtual_address_space_kb AS BIGINT) AS vas_kb "+
		"FROM sys.dm_os_process_memory"); row != nil {
		out["in_use_kb"] = row["in_use_kb"]
		out["vas_kb"] = row["vas_kb"]
	}
	if rows, err := db.Query("SELECT servicename, startup_type_desc, status_desc, service_account " +
		"FROM sys.dm_server_services"); err == nil {
		out["services"] = rows
	}
	return out
}

func waitStats(db Querier) *WaitStatsData {
	rows, err := db.Query(sqlWaits)
	if err != nil {
		return &WaitStatsData{}
	}
	ws := &WaitStatsData{}
	for _, r := range rows {
		wt := gs(r, "wait_type")
		if benignWaits[wt] {
			continue
		}
		wr := WaitRow{Type: wt, WaitMS: gfi(r, "wait_time_ms"),
			Tasks: gfi(r, "waiting_tasks_count"), SignalMS: gfi(r, "signal_wait_time_ms")}
		ws.Rows = append(ws.Rows, wr)
		ws.TotalMS += wr.WaitMS
		ws.SignalMS += wr.SignalMS
		ws.Tasks += wr.Tasks
	}
	sort.Slice(ws.Rows, func(i, j int) bool { return ws.Rows[i].WaitMS > ws.Rows[j].WaitMS })
	if len(ws.Rows) > 40 {
		ws.Rows = ws.Rows[:40]
	}
	return ws
}

func requestsLive(db Querier) *LiveData {
	live := &LiveData{}
	rows, err := db.Query(sqlRequests)
	if err != nil {
		return live
	}
	seenBlockers := map[int]bool{}
	for _, r := range rows {
		r["elapsed_s"] = mathRound1(gfi(r, "total_elapsed_time") / 1000)
		r["cpu_s"] = mathRound1(gfi(r, "cpu_time") / 1000)
		r["text_head"] = strHead(gs(r, "sql_text"), 300)
		b := gii(r, "blocking_session_id")
		if b > 0 {
			seenBlockers[b] = true
		}
	}
	if len(rows) > 60 {
		rows = rows[:60]
	}
	live.Requests = rows
	if len(seenBlockers) > 0 {
		ids := make([]string, 0, len(seenBlockers))
		for id := range seenBlockers {
			ids = append(ids, fmt.Sprint(id))
		}
		q := "SELECT s.session_id, s.login_name, s.host_name, s.program_name, s.status, " +
			"DB_NAME(s.database_id) AS db_name, s.open_transaction_count AS tran_count, " +
			"t.text AS sql_text, s.wait_type, s.wait_time " +
			"FROM sys.dm_exec_sessions s WITH (NOLOCK) " +
			"LEFT JOIN sys.dm_exec_connections c WITH (NOLOCK) ON c.session_id = s.session_id " +
			"OUTER APPLY sys.dm_exec_sql_text(c.most_recent_sql_handle) t " +
			"WHERE s.session_id IN (" + strings.Join(ids, ",") + ")"
		if rows2, err := db.Query(q); err == nil {
			live.Blockers = rows2
		}
	}
	return live
}

func mathRound1(v float64) float64 { return float64(int64(v*10+0.5*sign(v))) / 10 }

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

func qstats(db Querier) *QStatsData {
	qs := &QStatsData{}
	base := "SELECT TOP 15 qs.execution_count, qs.total_worker_time, qs.total_elapsed_time, " +
		"qs.total_logical_reads, qs.total_physical_reads, qs.total_logical_writes, " +
		"%s qs.creation_time, qs.last_execution_time, DB_NAME(st.dbid) AS db_name, " +
		"OBJECT_SCHEMA_NAME(st.objectid, st.dbid) AS schema_name, " +
		"OBJECT_NAME(st.objectid, st.dbid) AS object_name, st.text AS sql_text " +
		"FROM sys.dm_exec_query_stats qs WITH (NOLOCK) " +
		"OUTER APPLY sys.dm_exec_sql_text(qs.sql_handle) st %s " +
		"ORDER BY %s DESC"
	post := func(rows []map[string]any) []map[string]any {
		for _, r := range rows {
			ec := gfi(r, "execution_count")
			if ec > 0 {
				r["avg_elapsed_ms"] = math.Round(gfi(r, "total_elapsed_time")/ec*10) / 10
				r["avg_worker_ms"] = math.Round(gfi(r, "total_worker_time")/ec*10) / 10
				r["avg_reads"] = int64(gfi(r, "total_logical_reads") / ec)
			} else {
				r["avg_elapsed_ms"], r["avg_worker_ms"], r["avg_reads"] = 0.0, 0.0, int64(0)
			}
			r["text_head"] = strHead(gs(r, "sql_text"), 300)
		}
		return rows
	}
	qs.ByCPU = post(queryRowsAny(db, []string{
		fmt.Sprintf(base, "qs.total_spills,", "", "qs.total_worker_time"),
		fmt.Sprintf(base, "", "", "qs.total_worker_time")}))
	qs.ByAvg = post(queryRowsAny(db, []string{
		fmt.Sprintf(base, "qs.total_spills,", "WHERE qs.execution_count >= 10",
			"(qs.total_elapsed_time / qs.execution_count)"),
		fmt.Sprintf(base, "", "WHERE qs.execution_count >= 10",
			"(qs.total_elapsed_time / qs.execution_count)")}))
	qs.ByReads = post(queryRowsAny(db, []string{
		fmt.Sprintf(base, "qs.total_spills,", "", "qs.total_logical_reads"),
		fmt.Sprintf(base, "", "", "qs.total_logical_reads")}))
	if rows, err := db.Query(
		"SELECT TOP 10 qs.query_hash, COUNT(DISTINCT qs.plan_handle) AS plan_count, " +
			"SUM(qs.execution_count) AS execs, " +
			"MIN(CAST(st.text AS NVARCHAR(4000))) AS sql_text " +
			"FROM sys.dm_exec_query_stats qs WITH (NOLOCK) " +
			"OUTER APPLY sys.dm_exec_sql_text(qs.sql_handle) st " +
			"GROUP BY qs.query_hash HAVING COUNT(DISTINCT qs.plan_handle) >= 5 " +
			"ORDER BY plan_count DESC"); err == nil {
		qs.DupPlans = rows
	}
	return qs
}

// queryRowsAny — queryAny, не паникующий на пустом результате.
func queryRowsAny(db Querier, sqls []string) []map[string]any {
	rows, err := queryAny(db, sqls)
	if err != nil {
		return nil
	}
	return rows
}

func planIssues(db Querier) *PlanIssuesData {
	pi := &PlanIssuesData{}
	rows, err := db.Query(
		"SELECT TOP 25 CAST(qp.query_plan AS NVARCHAR(MAX)) AS plan_xml, " +
			"st.text AS sql_text FROM (" +
			" SELECT TOP 25 qs.plan_handle, qs.sql_handle, qs.total_worker_time " +
			" FROM sys.dm_exec_query_stats qs WITH (NOLOCK) " +
			" ORDER BY qs.total_worker_time DESC) qs " +
			"OUTER APPLY sys.dm_exec_query_plan(qs.plan_handle) qp " +
			"OUTER APPLY sys.dm_exec_sql_text(qs.sql_handle) st " +
			"WHERE qp.query_plan IS NOT NULL")
	if err != nil {
		return pi
	}
	// encoding/xml не сопоставляет теги в default-namespace и отвергает
	// декларацию с encoding="utf-16" — срезаем и то, и другое.
	reXmlns := regexp.MustCompile(`(?s)^<\?xml[^>]*\?>|\sxmlns="[^"]*"`)
	for _, r := range rows {
		xmlText := gs(r, "plan_xml")
		if xmlText == "" || len(xmlText) > 3_000_000 {
			continue
		}
		head := strHead(gs(r, "sql_text"), 200)
		var plan planRoot
		if err := xml.Unmarshal([]byte(reXmlns.ReplaceAllString(xmlText, "")), &plan); err != nil {
			continue
		}
		for _, pl := range plan.Plans {
			for _, w := range pl.Warnings {
				pi.MissingInPlans += len(w.MissingIndexes)
				for _, c := range w.Converts {
					pi.Converts = append(pi.Converts, ConvertInfo{
						Issue: c.ConvertIssue, Expr: strHead(c.Expression, 200), SQL: head})
				}
				for range w.Spills {
					pi.Spills = append(pi.Spills, ConvertInfo{SQL: head})
				}
				pi.NoJoin += len(w.NoJoin)
			}
		}
	}
	if len(pi.Converts) > 30 {
		pi.Converts = pi.Converts[:30]
	}
	if len(pi.Spills) > 15 {
		pi.Spills = pi.Spills[:15]
	}
	return pi
}

type planRoot struct {
	Plans []struct {
		Warnings []planWarnings `xml:"QueryPlan>Warnings"`
	} `xml:"BatchSequence>Batch>Statements>StmtSimple"`
}

type planWarnings struct {
	Converts []struct {
		ConvertIssue string `xml:"ConvertIssue,attr"`
		Expression   string `xml:"Expression,attr"`
	} `xml:"PlanAffectingConvert"`
	Spills         []struct{} `xml:"SpillToTempDb"`
	NoJoin         []struct{} `xml:"NoJoinPredicate"`
	MissingIndexes []struct{} `xml:"MissingIndexes"`
}

func userDBs(db Querier) []string {
	rows, err := db.Query("SELECT name FROM sys.databases " +
		"WHERE state_desc = N'ONLINE' AND database_id > 4 " +
		"AND source_database_id IS NULL ORDER BY name")
	if err != nil {
		return nil
	}
	out := []string{}
	for _, r := range rows {
		out = append(out, gs(r, "name"))
	}
	return out
}

func qdb(name string) string { return "[" + strings.ReplaceAll(name, "]", "]]") + "]" }

func sqlStr(v string) string { return "N'" + strings.ReplaceAll(v, "'", "''") + "'" }

func missingIndexes(db Querier) []map[string]any {
	var out []map[string]any
	for _, dbname := range firstN(userDBs(db), 80) {
		q := "SELECT d.statement AS obj, d.equality_columns, d.inequality_columns, " +
			"d.included_columns, gs.unique_compiles, gs.user_seeks, gs.user_scans, " +
			"gs.avg_user_impact, gs.last_user_seek FROM " + qdb(dbname) +
			".sys.dm_db_missing_index_details d JOIN " + qdb(dbname) +
			".sys.dm_db_missing_index_groups g ON g.index_handle = d.index_handle JOIN " +
			qdb(dbname) + ".sys.dm_db_missing_index_group_stats gs " +
			"ON gs.group_handle = g.index_group_handle ORDER BY gs.user_seeks DESC"
		rows, err := db.Query(q)
		if err != nil {
			continue
		}
		for _, r := range rows {
			r["db"] = dbname
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return gfi(out[i], "user_seeks")*gfi(out[i], "avg_user_impact") >
			gfi(out[j], "user_seeks")*gfi(out[j], "avg_user_impact")
	})
	if len(out) > 30 {
		out = out[:30]
	}
	re := regexp.MustCompile(`[^\w]`)
	for _, r := range out {
		obj := strings.TrimSpace(gs(r, "obj"))
		cols := strings.TrimSpace(gs(r, "equality_columns"))
		if cols == "" {
			cols = strings.TrimSpace(gs(r, "inequality_columns"))
		}
		inc := strings.TrimSpace(gs(r, "included_columns"))
		if obj == "" || cols == "" {
			r["ddl"] = ""
			continue
		}
		short := re.ReplaceAllString(lastPart(obj, "."), "_")
		if len(short) > 20 {
			short = short[:20]
		}
		ddl := fmt.Sprintf("CREATE INDEX [ix_mi_%s_%s] ON %s (%s)", short,
			re.ReplaceAllString(cols, ""), obj, cols)
		if inc != "" {
			ddl += " INCLUDE (" + inc + ")"
		}
		r["ddl"] = ddl
	}
	return out
}

func lastPart(s, sep string) string {
	parts := strings.Split(s, sep)
	return parts[len(parts)-1]
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

func fragmentation(db Querier) []map[string]any {
	var out []map[string]any
	for _, dbname := range firstN(userDBs(db), 60) {
		q := "SELECT TOP 30 OBJECT_SCHEMA_NAME(ips.object_id) AS sch, " +
			"OBJECT_NAME(ips.object_id) AS tbl, i.name AS ix_name, " +
			"CAST(ips.avg_fragmentation_in_percent AS FLOAT) AS frag, ips.page_count, " +
			"i.fill_factor, i.type_desc FROM sys.dm_db_index_physical_stats(" +
			"DB_ID(" + sqlStr(dbname) + "), NULL, NULL, NULL, N'LIMITED') ips " +
			"JOIN sys.indexes i ON i.object_id = ips.object_id AND i.index_id = ips.index_id " +
			"WHERE ips.index_id > 0 AND ips.index_level = 0 AND ips.page_count >= 512 " +
			"AND ips.avg_fragmentation_in_percent >= 15 " +
			"ORDER BY ips.avg_fragmentation_in_percent DESC"
		rows, err := db.Query(q)
		if err != nil {
			continue
		}
		for _, r := range rows {
			r["db"] = dbname
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return gfi(out[i], "frag") > gfi(out[j], "frag") })
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func statsStale(db Querier) []map[string]any {
	var out []map[string]any
	for _, dbname := range firstN(userDBs(db), 80) {
		q := "SELECT OBJECT_SCHEMA_NAME(s.object_id) AS sch, OBJECT_NAME(s.object_id) AS tbl, " +
			"s.name AS stat_name, sp.last_updated, sp.rows, sp.rows_sampled, " +
			"sp.modification_counter, sp.no_recompute FROM " + qdb(dbname) + ".sys.stats s " +
			"JOIN " + qdb(dbname) + ".sys.objects o ON o.object_id = s.object_id " +
			"CROSS APPLY " + qdb(dbname) + ".sys.dm_db_stats_properties(s.object_id, s.stats_id) sp " +
			"WHERE o.is_ms_shipped = 0 AND sp.rows >= 5000"
		rows, err := db.Query(q)
		if err != nil {
			continue
		}
		for _, r := range rows {
			r["db"] = dbname
			out = append(out, r)
		}
	}
	return out
}

func unusedIndexes(db Querier) []map[string]any {
	var out []map[string]any
	for _, dbname := range firstN(userDBs(db), 60) {
		q := "SELECT OBJECT_SCHEMA_NAME(i.object_id) AS sch, OBJECT_NAME(i.object_id) AS tbl, " +
			"i.name AS ix_name, i.type_desc, " +
			"ISNULL(us.user_seeks,0)+ISNULL(us.user_scans,0)+ISNULL(us.user_lookups,0) AS reads, " +
			"ISNULL(us.user_updates,0) AS updates, " +
			"SUM(ps.used_page_count)*8/1024.0 AS size_mb " +
			"FROM " + qdb(dbname) + ".sys.indexes i JOIN " + qdb(dbname) +
			".sys.objects o ON o.object_id = i.object_id " +
			"LEFT JOIN " + qdb(dbname) + ".sys.dm_db_index_usage_stats us ON us.database_id = DB_ID() " +
			"AND us.object_id = i.object_id AND us.index_id = i.index_id " +
			"JOIN " + qdb(dbname) + ".sys.dm_db_partition_stats ps ON ps.object_id = i.object_id " +
			"AND ps.index_id = i.index_id " +
			"WHERE o.is_ms_shipped = 0 AND i.index_id > 0 AND i.is_primary_key = 0 " +
			"AND i.is_unique_constraint = 0 AND i.is_unique = 0 " +
			"GROUP BY i.object_id, i.index_id, i.name, i.type_desc, us.user_seeks, " +
			"us.user_scans, us.user_lookups, us.user_updates " +
			"HAVING ISNULL(us.user_seeks,0)+ISNULL(us.user_scans,0)+ " +
			"ISNULL(us.user_lookups,0) = 0 AND ISNULL(us.user_updates,0) >= 500"
		rows, err := db.Query(q)
		if err != nil {
			continue
		}
		for _, r := range rows {
			r["db"] = dbname
			out = append(out, r)
		}
	}
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

func databases(db Querier) (*DBsData, error) {
	res := &DBsData{}
	dbs, err := db.Query(
		"SELECT d.name, d.state_desc, d.recovery_model_desc, d.log_reuse_wait_desc, " +
			"d.compatibility_level, d.page_verify_option_desc, " +
			"d.is_read_committed_snapshot_on, d.is_auto_shrink_on, d.is_auto_close_on, " +
			"d.is_auto_create_stats_on, d.is_auto_update_stats_on, " +
			"d.snapshot_isolation_state_desc, d.log_reuse_wait, " +
			"CAST(CASE WHEN d.database_id <= 4 THEN 1 ELSE 0 END AS INT) AS is_system " +
			"FROM sys.databases d WHERE d.state_desc = N'ONLINE' ORDER BY d.database_id")
	if err != nil {
		return nil, err
	}
	files, err2 := db.Query(
		"SELECT DB_NAME(f.database_id) AS db, f.name AS logical_name, f.type_desc, " +
			"CAST(f.size AS BIGINT)*8/1024.0 AS size_mb, f.growth, f.is_percent_growth, " +
			"f.physical_name FROM sys.master_files f ORDER BY f.database_id, f.type_desc")
	if err2 != nil {
		res.Databases = dbs
		return res, err2
	}
	sizes := map[string]map[string]float64{}
	for _, f := range files {
		key := gs(f, "db")
		if sizes[key] == nil {
			sizes[key] = map[string]float64{"data_mb": 0, "log_mb": 0}
		}
		if gs(f, "type_desc") == "LOG" {
			sizes[key]["log_mb"] += gfi(f, "size_mb")
		} else {
			sizes[key]["data_mb"] += gfi(f, "size_mb")
		}
	}
	for _, d := range dbs {
		name := gs(d, "name")
		if s, ok := sizes[name]; ok {
			d["data_mb"] = s["data_mb"]
			d["log_mb"] = s["log_mb"]
		}
		if row := queryRow(db, "SELECT CAST(total_log_size_in_bytes/1048576.0 AS FLOAT) AS log_mb, "+
			"CAST(used_log_space_in_percent AS FLOAT) AS used_pct FROM "+
			qdb(name)+".sys.dm_db_log_space_usage"); row != nil {
			d["log_used_pct"] = row["used_pct"]
		}
		if row := queryRow(db, "SELECT COUNT(*) AS vlf FROM "+qdb(name)+
			".sys.dm_db_log_info(DB_ID())"); row != nil {
			d["vlf"] = row["vlf"]
		}
	}
	res.Databases = dbs
	res.Files = files
	return res, nil
}

func tempdbInfo(db Querier) *TempdbData {
	td := &TempdbData{}
	td.Files, _ = db.Query("SELECT name, CAST(size AS BIGINT)*8/1024.0 AS size_mb, " +
		"physical_name, type_desc FROM tempdb.sys.database_files")
	td.Space = queryRow(db, sqlTempDB)
	if td.Space == nil {
		td.Space = map[string]any{}
	}
	td.SnapshotTR, _ = db.Query(
		"SELECT TOP 5 session_id, elapsed_time_seconds, DB_NAME(database_id) AS db FROM " +
			"sys.dm_tran_active_snapshot_database_transactions ORDER BY elapsed_time_seconds DESC")
	return td
}

func queryStore(db Querier) []map[string]any {
	var out []map[string]any
	for _, dbname := range firstN(userDBs(db), 80) {
		q := "SELECT actual_state_desc FROM " + qdb(dbname) + ".sys.database_query_store_options"
		if row := queryRow(db, q); row != nil {
			out = append(out, map[string]any{"db": dbname, "state": gs(row, "actual_state_desc")})
		}
	}
	return out
}

func backups(db Querier) ([]map[string]any, error) {
	rows, err := queryAny(db, []string{
		"SELECT d.name AS db, d.recovery_model_desc, bf.last_full, bd.last_diff, bl.last_log " +
			"FROM (SELECT name, recovery_model_desc FROM sys.databases " +
			"WHERE database_id > 4 AND state_desc = N'ONLINE') d " +
			"LEFT JOIN (SELECT database_name, MAX(backup_finish_date) AS last_full " +
			"FROM msdb.dbo.backupset WHERE type = 'D' AND is_copy_only = 0 " +
			"GROUP BY database_name) bf ON bf.database_name = d.name " +
			"LEFT JOIN (SELECT database_name, MAX(backup_finish_date) AS last_diff " +
			"FROM msdb.dbo.backupset WHERE type = 'I' AND is_copy_only = 0 " +
			"GROUP BY database_name) bd ON bd.database_name = d.name " +
			"LEFT JOIN (SELECT database_name, MAX(backup_finish_date) AS last_log " +
			"FROM msdb.dbo.backupset WHERE type = 'L' " +
			"GROUP BY database_name) bl ON bl.database_name = d.name ORDER BY d.name"})
	for _, r := range rows {
		for _, k := range []string{"last_full", "last_diff", "last_log"} {
			r[k+"_ago_h"] = nil
			if t, ok := gtime(r, k); ok {
				r[k+"_ago_h"] = time.Since(t).Hours()
			}
		}
	}
	return rows, err
}

func jobsFailed(db Querier) []map[string]any {
	rows, err := db.Query(
		"SELECT j.name AS job, h.step_id, h.step_name, h.run_date, h.run_time, " +
			"h.run_status, h.message FROM msdb.dbo.sysjobhistory h " +
			"JOIN msdb.dbo.sysjobs j ON j.job_id = h.job_id " +
			"WHERE h.run_status = 0 AND h.step_id > 0 " +
			"AND h.run_date >= CONVERT(INT, CONVERT(VARCHAR(8), DATEADD(DAY, -2, GETDATE()), 112)) " +
			"ORDER BY h.run_date DESC, h.run_time DESC")
	if err != nil || len(rows) == 0 {
		return nil
	}
	if len(rows) > 40 {
		rows = rows[:40]
	}
	for _, r := range rows {
		rd := fmt.Sprintf("%08d", gii(r, "run_date"))
		rt := fmt.Sprintf("%06d", gii(r, "run_time"))
		t, err := time.ParseInLocation("20060102150405", rd+rt, time.Local)
		if err == nil {
			r["run_dt"] = t
		}
		r["message_head"] = strHead(gs(r, "message"), 300)
	}
	return rows
}

// ---------------- взаимоблокировки (system_health) ----------------

type rbEvent struct {
	Name string   `xml:"name,attr"`
	Data []rbData `xml:"data"`
}

type rbData struct {
	Name  string `xml:"name,attr"`
	Value struct {
		Inner string `xml:",innerxml"`
	} `xml:"value"`
}

type dlGraph struct {
	Victims []struct {
		ID string `xml:"id,attr"`
	} `xml:"victimProcess"`
	ProcessList struct {
		Processes []dlProcess `xml:"process"`
	} `xml:"process-list"`
	ResourceList struct {
		ObjectNames []struct {
			Text string `xml:",chardata"`
		} `xml:"objectname"`
	} `xml:"resource-list"`
}

type dlProcess struct {
	ID               string `xml:"id,attr"`
	Spid             string `xml:"spid,attr"`
	Hostname         string `xml:"hostname,attr"`
	Loginname        string `xml:"loginname,attr"`
	Clientapp        string `xml:"clientapp,attr"`
	Isolationlevel   string `xml:"isolationlevel,attr"`
	Waitresource     string `xml:"waitresource,attr"`
	Waittime         string `xml:"waittime,attr"`
	Logused          string `xml:"logused,attr"`
	Currentdatabase  string `xml:"currentdatabase,attr"`
	Lastbatchstarted string `xml:"lastbatchstarted,attr"`
	Inputbuf         string `xml:"inputbuf"`
}

func deadlocks(db Querier) []DeadlockInfo {
	rows, err := db.Query(
		"SELECT CAST(xest.target_data AS NVARCHAR(MAX)) AS td " +
			"FROM sys.dm_xe_session_targets xest " +
			"JOIN sys.dm_xe_sessions xe ON xe.address = xest.event_session_address " +
			"WHERE xe.name = N'system_health' AND xest.target_name = N'ring_buffer'")
	if err != nil || len(rows) == 0 {
		return nil
	}
	td := gs(rows[0], "td")
	if len(td) == 0 || len(td) > 8_000_000 {
		return nil
	}
	var ring struct {
		Events []rbEvent `xml:"Event"`
	}
	if err := xml.Unmarshal([]byte(td), &ring); err != nil {
		return nil
	}
	var out []DeadlockInfo
	for _, ev := range ring.Events {
		if ev.Name != "xml_deadlock_report" {
			continue
		}
		for _, data := range ev.Data {
			if data.Name != "xml_report" {
				continue
			}
			if dl := parseDeadlock(data.Value.Inner); dl != nil {
				out = append(out, *dl)
			}
		}
		if len(out) >= 25 {
			break
		}
	}
	return out
}

func parseDeadlock(graphXML string) *DeadlockInfo {
	var g dlGraph
	if err := xml.Unmarshal([]byte(graphXML), &g); err != nil {
		return nil
	}
	dl := &DeadlockInfo{Objects: []string{}}
	if len(g.Victims) > 0 {
		dl.Victim = g.Victims[0].ID
	}
	for _, p := range g.ProcessList.Processes {
		proc := DeadlockProc{
			ID: p.ID, Spid: p.Spid, Hostname: p.Hostname, Loginname: p.Loginname,
			Clientapp: p.Clientapp, Isolation: p.Isolationlevel,
			Waitresource: p.Waitresource, Waittime: p.Waittime, Logused: p.Logused,
			CurrentDB: p.Currentdatabase, LastBatch: p.Lastbatchstarted,
			Inputbuf: strHead(p.Inputbuf, 300), Victim: p.ID == dl.Victim,
		}
		dl.Processes = append(dl.Processes, proc)
		if dl.Time == nil {
			if t, err := time.ParseInLocation("2006-01-02T15:04:05",
				p.Lastbatchstarted[:min(19, len(p.Lastbatchstarted))], time.Local); err == nil {
				dl.Time = &t
			}
		}
	}
	seen := map[string]bool{}
	for _, o := range g.ResourceList.ObjectNames {
		t := strings.TrimSpace(o.Text)
		if t != "" && !seen[t] {
			seen[t] = true
			dl.Objects = append(dl.Objects, t)
		}
	}
	if len(dl.Objects) > 6 {
		dl.Objects = dl.Objects[:6]
	}
	return dl
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------- журнал ошибок ----------------

var errorLogPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)taking longer than \d+ seconds`),
	regexp.MustCompile(`(?i)autogrow of database`),
	regexp.MustCompile(`(?i)Error:\s*(1[7-9]|2\d)`),
	regexp.MustCompile(`(?i)not enough memory`),
	regexp.MustCompile(`(?i)Failed.*(allocate|reserve)`),
	regexp.MustCompile(`(?i)Scheduler \d+ appears`),
	regexp.MustCompile(`(?i)SQL Server has encountered`),
	regexp.MustCompile(`(?i)corrupt`),
	regexp.MustCompile(`(?i)suspect`),
	regexp.MustCompile(`(?i)operating system error`),
	regexp.MustCompile(`(?i)I/O error`),
	regexp.MustCompile(`(?i)failed to`),
	regexp.MustCompile(`(?i)insufficient system memory`),
}

func errorLog(db Querier) ([]map[string]any, error) {
	// sp_readerrorlog возвращает большой набор строк — срезаем до лимита.
	var rows []map[string]any
	var err error
	if d, ok := db.(*Database); ok {
		rows, err = d.QueryTimeout("EXEC sys.sp_readerrorlog 0, 1", 180*time.Second)
	} else {
		rows, err = db.Query("EXEC sys.sp_readerrorlog 0, 1")
	}
	if err != nil {
		return nil, err
	}
	return filterErrorLog(rows), nil
}

func filterErrorLog(rows []map[string]any) []map[string]any {
	out := []map[string]any{}
	limit := 60000
	if len(rows) > limit {
		rows = rows[:limit]
	}
	for _, r := range rows {
		text := gs(r, "text")
		for _, p := range errorLogPatterns {
			if p.MatchString(text) {
				out = append(out, map[string]any{
					"date": r["logdate"], "process": r["processinfo"],
					"text": strHead(text, 400)})
				break
			}
		}
		if len(out) >= 300 {
			break
		}
	}
	return out
}
