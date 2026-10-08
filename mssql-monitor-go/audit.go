// Evidence-based configuration audit derived from the two Habr articles.
// Unknown / manual checks are not failures and never reduce the health score.
package main

import (
	"fmt"
	"sort"
	"strings"
)

const articleDeployment = "https://habr.com/ru/articles/1079630/"
const articleOperation = "https://habr.com/ru/articles/1080996/"
const rcsiAdvice = "Сначала проверьте режим блокировок и совместимость конфигурации 1С. RCSI влияет только на READ COMMITTED, не устраняет REPEATABLE READ/SERIALIZABLE и конфликты писателей. Перевод в управляемый режим требует доработки и тестирования кода 1С. Оцените место под версии строк в tempdb (при ADR — PVS), длинные транзакции и окно обслуживания; согласуйте изменение с DBA. Не включайте принудительный откат рабочих транзакций."
const parallelAdvice = "Подберите MAXDOP по доступным online CPU и NUMA, а cost threshold — по планам, CPU и дельтам ожиданий за рабочий период. Для тяжёлой ERP параллелизм полезен отчётам; для множества бухгалтерских баз важнее конкуренция. Универсальных 1, 8 или 25–50 нет. Меняйте один параметр за раз, сравнивайте с эталонным периодом и проверяйте регрессии в Query Store. Сам по себе CXPACKET не является ошибкой."

type AuditCheck struct {
	ID, Parameter, Actual, Expected, Status, Action, Source string
}

func workloadName(profile string) string {
	switch profile {
	case "erp":
		return "1С: тяжёлая ERP"
	case "accounting":
		return "1С: множество бухгалтерских баз"
	default:
		return "не задан — применимость настроек 1С нужно подтвердить"
	}
}
func workloadAdvice(profile string) string {
	switch profile {
	case "erp":
		return "Профиль ERP: отдельно измеряйте тяжёлые отчёты, конкуренцию с OLTP, блокировки, гранты памяти и tempdb. Не отключайте полезный параллелизм отчётов без сравнения длительности. "
	case "accounting":
		return "Профиль множества баз: оцените общий пик отчётности, распределение памяти между базами, конкуренцию запросов и завершение обслуживания всех баз в ночное окно. Проверьте независимое восстановление одной базы. "
	default:
		return "Профиль не задан: сначала уточните назначение и нагрузку; не переносите настройки ERP на множество небольших баз автоматически. "
	}
}

func validWorkload(s string) bool { return s == "unknown" || s == "erp" || s == "accounting" }

func BuildAudit(d *DeepData) []AuditCheck {
	var out []AuditCheck
	add := func(id, param, actual, expected, status, action, source string) {
		if actual == "" {
			actual = "нет данных"
			status = "unknown"
		}
		out = append(out, AuditCheck{id, param, actual, expected, status, action, source})
	}
	profile := d.Workload == "erp" || d.Workload == "accounting"
	coll := gs(d.Server, "collation")
	status := "manual"
	if profile && coll != "" {
		status = "ok"
		if !strings.EqualFold(coll, "Cyrillic_General_CI_AS") {
			status = "mismatch"
		}
	}
	add("COLLATION", "Collation инстанса", coll, "Для 1С по статье: Cyrillic_General_CI_AS", status,
		"Согласуйте требования вашей версии 1С. Смена collation инстанса — отдельная миграция системных/пользовательских БД с тестом восстановления, не обычный ALTER DATABASE.", articleDeployment)

	// Configuration values are shown even when collection was unavailable.
	cfg := map[string]map[string]any{}
	for _, c := range d.Configs {
		cfg[strings.ToLower(gs(c, "name"))] = c
	}
	for _, name := range []string{"max server memory (mb)", "max degree of parallelism", "cost threshold for parallelism"} {
		actual := gs(cfg[name], "value_in_use")
		expected := "По профилю нагрузки, CPU, NUMA и планам"
		action := workloadAdvice(d.Workload) + parallelAdvice
		source := articleOperation
		if name == "max server memory (mb)" {
			expected = "Лимит с резервом ОС, потоков и других служб"
			action = "Рассчитайте по пику потребления. 6 ГБ резерва при 8 ядрах/32 ГБ в статье — пример, не универсальное правило; max server memory не ограничивает весь процесс SQL. Проверьте LPIM и резерв памяти гипервизора."
			source = articleDeployment
		}
		add("CFG-"+name, name, actual, expected, "manual", action, source)
	}

	services, _ := d.SysInfo["services"].([]map[string]any)
	if len(services) == 0 {
		add("IFI", "Instant File Initialization", "", "ON для службы Database Engine", "unknown", "Проверьте право Perform Volume Maintenance Tasks у служебной учётной записи; отсутствие прав DMV не означает OFF.", articleDeployment)
	}
	for i, s := range services {
		if !strings.HasPrefix(strings.ToLower(gs(s, "servicename")), "sql server (") {
			continue
		}
		actual := gs(s, "instant_file_initialization_enabled")
		st := "unknown"
		if actual == "Y" {
			st = "ok"
		} else if actual == "N" {
			st = "mismatch"
		}
		add(fmt.Sprintf("IFI-%d", i), "IFI / "+gs(s, "servicename"), actual, "Y", st, "Выдайте Perform Volume Maintenance Tasks служебной учётной записи через согласованное изменение. IFI прежде всего относится к файлам данных; журнал имеет ограничения по версии и размеру роста.", articleDeployment)
	}

	if d.DBs == nil || len(d.DBs.Databases) == 0 {
		add("DB-DATA", "Параметры баз (checkpoint, RCSI, recovery)", "", "Доступ к sys.databases", "unknown", "Проверьте права и ошибки сбора; отсутствующие значения нельзя считать выключенными опциями.", articleOperation)
	} else {
		for _, db := range d.DBs.Databases {
			if gii(db, "is_system") == 1 {
				continue
			}
			name := gs(db, "name")
			actual := gs(db, "target_recovery_time_in_seconds")
			st := "manual"
			if major, ok := gi(d.Server, "major"); ok && major >= 13 && actual != "" {
				st = "ok"
				if gii(db, "target_recovery_time_in_seconds") == 0 {
					st = "mismatch"
				}
			}
			add("CHECKPOINT-"+name, name+" / target_recovery_time_in_seconds", actual, "SQL Server 2016+: indirect checkpoint, обычно 60 с", st, "Ноль означает automatic checkpoint. Для перенесённой базы оцените переход на 60 с с DBA: измерьте задержки записи до/после, подтвердите версию и профиль нагрузки.", articleOperation)
			actual = gs(db, "is_read_committed_snapshot_on")
			st = "manual"
			if actual == "1" || strings.EqualFold(actual, "true") {
				st = "ok"
			}
			add("RCSI-"+name, name+" / RCSI", actual, "ON для подходящей нагрузки READ COMMITTED; применимость проверить", st, rcsiAdvice, articleOperation)
			add("RECOVERY-"+name, name+" / recovery model", gs(db, "recovery_model_desc"), "Прод: FULL + бэкапы журнала по RPO; тест: обычно SIMPLE", "manual", "Уточните назначение базы и RPO/RTO. Проверьте расписание и тест восстановления. Не переключайте в SIMPLE для освобождения журнала: это разрывает цепочку восстановления. Наличие/отсутствие истории msdb не доказывает состояние внешней системы бэкапов.", articleOperation)
		}
	}

	if len(d.QueryStore) == 0 {
		add("QS-DATA", "Query Store", "", "Сбор истории на продуктивных базах SQL 2016+", "unknown", "Проверьте версию, права и фактическое состояние каждой базы. С SQL 2017 доступна история ожиданий запросов.", articleOperation)
	}
	for _, q := range d.QueryStore {
		st := "manual"
		actual := gs(q, "state")
		switch actual {
		case "READ_WRITE":
			st = "ok"
		case "OFF", "READ_ONLY", "ERROR":
			st = "mismatch"
		}
		add("QS-"+gs(q, "db"), gs(q, "db")+" / Query Store", actual, "READ_WRITE для накопления новой истории", st, "Для продуктивной базы согласуйте включение и лимит места/очистку. При READ_ONLY выясните readonly_reason и квоту; ON само по себе не гарантирует запись. Старые восстановленные базы сохраняют настройки.", articleOperation)
	}

	var files []map[string]any
	if d.Tempdb != nil {
		for _, f := range d.Tempdb.Files {
			if gs(f, "type_desc") == "ROWS" {
				files = append(files, f)
			}
		}
	}
	if len(files) == 0 {
		add("TEMP-LAYOUT", "Файлы данных tempdb", "", "Одинаковые размер и фиксированный шаг роста", "unknown", "Соберите tempdb.sys.database_files.", articleOperation)
	} else {
		sizes, growth := []string{}, []string{}
		sizeKnown, growthKnown := true, true
		equalSize, equalGrowth := true, true
		baseSize, sizeOK := gf(files[0], "size_mb")
		baseGrowth, growthOK := gf(files[0], "growth")
		for _, f := range files {
			sz, ok := gf(f, "size_mb")
			sizeKnown = sizeKnown && ok && sizeOK
			gr, gok := gf(f, "growth")
			pct, pok := gi(f, "is_percent_growth")
			growthKnown = growthKnown && gok && pok && growthOK
			equalSize = equalSize && sz == baseSize
			equalGrowth = equalGrowth && gr == baseGrowth && pct == 0 && gr > 0
			sizes = append(sizes, gs(f, "name")+"="+gs(f, "size_mb")+" МБ")
			growth = append(growth, fmt.Sprintf("%s: %s × 8КБ, percent=%s", gs(f, "name"), gs(f, "growth"), gs(f, "is_percent_growth")))
		}
		st := "unknown"
		if sizeKnown {
			st = "ok"
			if !equalSize {
				st = "mismatch"
			}
		}
		add("TEMP-SIZE", "Размеры tempdb", strings.Join(sizes, "; "), "Равный заранее выделенный размер", st, "Спланируйте преаллокацию по пику сортировок, временных таблиц и версий строк, оставьте запас тома. Не уменьшайте файлы автоматически.", articleOperation)
		st = "unknown"
		if growthKnown {
			st = "ok"
			if !equalGrowth {
				st = "mismatch"
			}
		}
		add("TEMP-GROWTH", "Авторост tempdb", strings.Join(growth, "; "), "Одинаковый ненулевой фиксированный шаг, не проценты", st, "Размер шага выбирается по нагрузке и запасу тома. Авторост — страховка, а не замена преаллокации.", articleOperation)
	}

	roles := map[string]map[string]bool{}
	for _, f := range d.FileVolumes {
		db := gs(f, "db")
		if db == "" || gs(f, "type_desc") == "" {
			continue
		}
		if db == "master" || db == "model" || db == "msdb" {
			continue
		}
		mount := gs(f, "volume_mount_point")
		if mount == "" {
			continue
		}
		role := "данные"
		if db == "tempdb" {
			role = "tempdb"
		} else if gs(f, "type_desc") == "LOG" {
			role = "журнал"
		}
		if roles[mount] == nil {
			roles[mount] = map[string]bool{}
		}
		roles[mount][role] = true
	}
	mounts := []string{}
	for mount := range roles {
		mounts = append(mounts, mount)
	}
	sort.Strings(mounts)
	if len(mounts) == 0 {
		add("IO-LAYOUT", "Размещение потоков IO", "", "Отдельные ресурсы для данных, журнала, tempdb и бэкапов", "unknown", "Нужна карта томов/LUN/контроллеров. По буквам дисков нельзя доказать физическое разделение.", articleDeployment)
	}
	for _, mount := range mounts {
		rs := []string{}
		for r := range roles[mount] {
			rs = append(rs, r)
		}
		sort.Strings(rs)
		st := "manual"
		if len(rs) > 1 {
			st = "mismatch"
		}
		add("IO-"+mount, "Том "+mount, strings.Join(rs, ", "), "Разделение конкурирующих потоков по ресурсам хранения", st, "Оцените фактические очереди и задержки. Разные тома не доказывают разные физические диски/LUN; проверьте RAID, контроллеры и общий datastore. Не переносите файлы автоматически.", articleDeployment)
	}
	for _, m := range []struct{ id, param, expected, action, source string }{
		{"HOST", "ОС и аппаратная платформа", "План питания, NTFS, исключения антивируса, LPIM, размещение 1С", "Проверьте High Performance в гостевой ОС/хосте/BIOS, 64КБ для томов SQL, согласованные исключения антивируса и LPIM при необходимости. Отделите ресурсы сервера 1С и его Temp от SQL. Эти параметры удалённого хоста не определяются локальными настройками машины монитора.", articleDeployment},
		{"VM", "Гипервизор", "Резерв памяти, CPU Ready, vNUMA, IO, снапшоты", "Проверьте memory reservation/ballooning, Dynamic Memory Hyper-V, переподписку CPU, CPU Ready и vNUMA с учётом версии гипервизора, контроллеры и datastore. Сопоставьте подвисания со снапшотами, stun, регламентами и расписанием бэкапов. DMV гостя этого не доказывают.", articleDeployment},
		{"SECURITY", "Учётные записи и сеть", "Минимальные права, управляемые учётные записи, ограниченный доступ", "Проверьте gMSA/доменные службы и Windows-аутентификацию, откажитесь от общего sa. Ограничьте SQL порт адресами сервера 1С и администраторов; проверьте статический порт, SQL Browser, ненужные протоколы, VPN для админов. Не публикуйте SQL/RDP в интернет.", articleOperation},
		{"BACKUP-SAFETY", "Защита резервных копий", "Шифрование, отдельный сертификат, изолированная копия, тест восстановления", "Проверьте шифрование с учётом версии/редакции, отдельную резервную копию сертификата и закрытого ключа, права к хранилищу и offline/immutable-копию. Хранение на другом сетевом пути само по себе не защищает от шифровальщика.", articleOperation},
		{"BASELINE", "Профиль и эталон нагрузки", "Измерения рабочего и спокойного периода, ответственный DBA", "ERP: тяжёлые отчёты, tempdb, блокировки. Множество баз: общий бюджет памяти, обслуживание всех баз за ночь и восстановление одной базы. Соберите дельты waits, CPU, PLE, IO и Query Store; сравнивайте с эталоном той же системы. Назначьте ответственного за алерты. Не покупайте железо по одному счётчику.", articleOperation},
	} {
		if m.id == "BASELINE" {
			m.action = workloadAdvice(d.Workload) + m.action
		}
		add(m.id, m.param, "не проверено автоматически", m.expected, "manual", m.action, m.source)
	}
	return out
}

func auditFindings(d *DeepData) []Finding {
	var out []Finding
	for _, c := range BuildAudit(d) {
		if c.Status != "mismatch" {
			continue
		}
		// Only measured deviations become findings; unknown/manual checks stay in the matrix.
		out = append(out, finding("AUDIT-"+c.ID, "info", "Аудит по статьям", c.Parameter+": отклонение от ориентира",
			[]string{"Фактически: " + c.Actual, "Ориентир: " + c.Expected, "Источник: " + c.Source},
			"Измеренное отклонение требует проверки применимости; само по себе не доказывает причину замедления.", c.Action, ""))
	}
	return out
}

func (r *Report) audit() string {
	labels := map[string]string{"ok": "Соответствует", "mismatch": "Отклонение", "unknown": "Нет данных", "manual": "Ручная проверка"}
	var rows []string
	for _, c := range BuildAudit(r.d) {
		rows = append(rows, tblCell(esc(c.Parameter), "")+tblCell(esc(c.Actual), "")+tblCell(esc(c.Expected), "")+tblCell(esc(labels[c.Status]), "")+tblCell(esc(c.Action), "")+tblCell("<a href='"+esc(c.Source)+"' target='_blank' rel='noopener'>Статья</a>", ""))
	}
	return "<section class='card' id='audit'><h2>Аудит по инструкциям: фактическое vs ориентир</h2><p><b>Профиль: </b>" + esc(workloadName(r.d.Workload)) + "</p><p>Источники: <a href='" + articleDeployment + "'>развёртывание</a> и <a href='" + articleOperation + "'>эксплуатация</a>. Это ориентиры, не универсальные нормативы. Нет данных ≠ несоответствие. Ручные проверки не снижают оценку. Все изменения — только после проверки DBA; монитор не меняет SQL Server.</p>" + tbl([]string{"Параметр", "Фактически", "Ориентир", "Статус", "Рекомендация / ограничения", "Источник"}, rows, "") + "</section>"
}

// A nil waits slice means an unavailable/first sample; a non-nil empty slice
// is a valid measured interval without waits. Ignore negative reset deltas.
func waitsForPeriod(samples []Sample) (*WaitStatsData, bool) {
	byType := map[string]*WaitRow{}
	measured := false
	for _, s := range samples {
		if s.Waits != nil {
			measured = true
		}
		for _, w := range s.Waits {
			if w.WaitMS < 0 || w.SignalMS < 0 || benignWaits[w.Type] {
				continue
			}
			if byType[w.Type] == nil {
				byType[w.Type] = &WaitRow{Type: w.Type}
			}
			r := byType[w.Type]
			r.WaitMS += w.WaitMS
			r.SignalMS += w.SignalMS
		}
	}
	d := &WaitStatsData{}
	for _, r := range byType {
		d.Rows = append(d.Rows, *r)
		d.TotalMS += r.WaitMS
		d.SignalMS += r.SignalMS
	}
	sort.Slice(d.Rows, func(i, j int) bool { return d.Rows[i].WaitMS > d.Rows[j].WaitMS })
	return d, measured
}
