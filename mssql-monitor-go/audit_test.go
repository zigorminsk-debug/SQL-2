package main

import (
	"strings"
	"testing"
)

func auditByID(d *DeepData, id string) AuditCheck {
	for _, c := range BuildAudit(d) {
		if c.ID == id {
			return c
		}
	}
	return AuditCheck{}
}
func TestAuditUnknownIsNotMismatch(t *testing.T) {
	d := &DeepData{}
	if f := auditFindings(d); len(f) != 0 {
		t.Fatalf("missing data produced findings: %+v", f)
	}
	if c := auditByID(d, "COLLATION"); c.Status != "unknown" {
		t.Fatalf("%+v", c)
	}
}
func TestCollationRequiresExplicitWorkload(t *testing.T) {
	d := &DeepData{Server: map[string]any{"collation": "SQL_Latin1_General_CP1_CI_AS"}}
	if c := auditByID(d, "COLLATION"); c.Status != "manual" {
		t.Fatal(c)
	}
	d.Workload = "erp"
	if c := auditByID(d, "COLLATION"); c.Status != "mismatch" {
		t.Fatal(c)
	}
	d.Server["collation"] = "Cyrillic_General_CI_AS"
	if c := auditByID(d, "COLLATION"); c.Status != "ok" {
		t.Fatal(c)
	}
}
func TestDatabaseAudit(t *testing.T) {
	d := &DeepData{Server: map[string]any{"major": 16}, DBs: &DBsData{Databases: []map[string]any{
		{"name": "a]'<script>", "target_recovery_time_in_seconds": 0, "is_read_committed_snapshot_on": 0},
		{"name": "good", "target_recovery_time_in_seconds": 60, "is_read_committed_snapshot_on": 1},
		{"name": "unknown"},
	}}, QueryStore: []map[string]any{{"db": "good", "state": "READ_ONLY"}}}
	for id, want := range map[string]string{"CHECKPOINT-a]'<script>": "mismatch", "CHECKPOINT-good": "ok", "CHECKPOINT-unknown": "unknown", "RCSI-a]'<script>": "manual", "QS-good": "mismatch"} {
		if c := auditByID(d, id); c.Status != want {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	html := (&Report{d: d}).audit()
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") {
		t.Fatal("unsafe HTML")
	}
}
func TestTempdbGrowthAndSizes(t *testing.T) {
	d := &DeepData{Tempdb: &TempdbData{Files: []map[string]any{
		{"name": "a", "type_desc": "ROWS", "size_mb": 100, "growth": 8192, "is_percent_growth": 0},
		{"name": "b", "type_desc": "ROWS", "size_mb": 100, "growth": 8192, "is_percent_growth": 0},
		{"name": "log", "type_desc": "LOG", "size_mb": 1, "growth": 1},
	}}}
	for _, id := range []string{"TEMP-SIZE", "TEMP-GROWTH"} {
		if c := auditByID(d, id); c.Status != "ok" {
			t.Fatal(c)
		}
	}
	d.Tempdb.Files[1]["size_mb"] = 200
	d.Tempdb.Files[1]["is_percent_growth"] = 1
	for _, id := range []string{"TEMP-SIZE", "TEMP-GROWTH"} {
		if c := auditByID(d, id); c.Status != "mismatch" {
			t.Fatal(c)
		}
	}
	delete(d.Tempdb.Files[1], "growth")
	if c := auditByID(d, "TEMP-GROWTH"); c.Status != "unknown" {
		t.Fatal(c)
	}
}
func TestFindingsUsePeriodAndSafeAdvice(t *testing.T) {
	d := &DeepData{WaitStats: &WaitStatsData{Rows: []WaitRow{{Type: "LCK_M_S", WaitMS: 900000}}, TotalMS: 900000},
		Configs: []map[string]any{{"name": "max degree of parallelism", "value_in_use": 0}}, SysInfo: map[string]any{"cpu_count": 16}}
	f := BuildFindings(d, []Sample{{Waits: []WaitDelta{}}}, []BlockingEvent{{SessionID: 1}}, nil)
	for _, v := range f {
		if v.ID == "WAIT-LOCK" {
			t.Fatal("used cumulative stats instead of empty measured interval")
		}
		if strings.Contains(v.SQLT, "ROLLBACK IMMEDIATE") || strings.Contains(v.SQLT, "parallelism', 8") {
			t.Fatal(v)
		}
	}
	if d.WaitStats.TotalMS != 900000 {
		t.Fatal("input mutated")
	}
	f = BuildFindings(d, nil, nil, nil)
	for _, v := range f {
		if strings.HasPrefix(v.ID, "WAIT-") && v.Severity != "info" {
			t.Fatal(v)
		}
	}
}
func TestGrowthUnits(t *testing.T) {
	for _, tc := range []struct {
		growth int
		tiny   bool
	}{{0, false}, {8191, true}, {8192, false}, {65536, false}} {
		d := &DeepData{DBs: &DBsData{Files: []map[string]any{{"growth": tc.growth, "is_percent_growth": 0}}}}
		found := false
		for _, f := range BuildFindings(d, nil, nil, nil) {
			if f.ID == "FILE-TINY" {
				found = true
			}
		}
		if found != tc.tiny {
			t.Fatalf("growth %d: tiny=%v", tc.growth, found)
		}
	}
}
func TestDemoReportIncludesAudit(t *testing.T) {
	d := collectDeep(NewDemoDatabase(), CheckOptions{Workload: "erp"}, nil, nil, nil)
	html, _, _, _ := BuildReport(d, nil, nil, nil, ReportMeta{})
	for _, text := range []string{articleDeployment, articleOperation, "Аудит по инструкциям", "Ручная проверка", "Нет данных", "1С: тяжёлая ERP"} {
		if !strings.Contains(html, text) {
			t.Fatal(text)
		}
	}
	if strings.Contains(html, "ROLLBACK IMMEDIATE") {
		t.Fatal("unsafe RCSI SQL")
	}
}

func TestPeriodIgnoresConsumerAndResetDeltas(t *testing.T) {
	waits, measured := waitsForPeriod([]Sample{{Waits: []WaitDelta{
		{Type: "CXCONSUMER", WaitMS: 1e8}, {Type: "WRITELOG", WaitMS: 100, SignalMS: 5},
		{Type: "PAGEIOLATCH_SH", WaitMS: -1},
	}}})
	if !measured || waits.TotalMS != 100 || len(waits.Rows) != 1 {
		t.Fatalf("%+v", waits)
	}
}

func TestFileVolumesDoNotProvePhysicalSeparation(t *testing.T) {
	d := &DeepData{FileVolumes: []map[string]any{
		{"db": "ERP", "type_desc": "ROWS", "volume_mount_point": "E:\\"},
		{"db": "ERP", "type_desc": "LOG", "volume_mount_point": "E:\\"},
		{"db": "tempdb", "type_desc": "ROWS", "volume_mount_point": "F:\\"},
	}}
	if c := auditByID(d, "IO-E:\\"); c.Status != "mismatch" {
		t.Fatal(c)
	}
	if c := auditByID(d, "IO-F:\\"); c.Status != "manual" {
		t.Fatal(c)
	}
}
