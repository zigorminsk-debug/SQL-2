// analysis_rules.go — правила выработки рекомендаций (data + таймлайн -> findings).
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func BuildFindings(d *DeepData, samples []Sample, blocking []BlockingEvent,
	longq []LongQuery) []Finding {
	// Prefer interval deltas; never mix them with cumulative waits since restart.
	copyData := *d
	d = &copyData
	periodWaits, measured := waitsForPeriod(samples)
	if measured {
		d.WaitStats = periodWaits
	}
	var F []Finding
	cfg := map[string]map[string]any{}
	for _, r := range d.Configs {
		cfg[strings.ToLower(gs(r, "name"))] = r
	}
	sysinfo := d.SysInfo
	physGB := 0.0
	if v, ok := gf(sysinfo, "phys_kb"); ok {
		physGB = v / 1048576.0
	}
	cpuCount := 0
	if v, ok := gi(sysinfo, "cpu_count"); ok {
		cpuCount = v
	}
	has := func(id string) bool {
		for _, f := range F {
			if f.ID == id {
				return true
			}
		}
		return false
	}

	// ---------- memory and parallelism: observations, not universal prescriptions ----------
	if m := cfg["max server memory (mb)"]; m != nil && physGB > 0 {
		if cur, ok := gf(m, "value_in_use"); ok && (cur >= 2147483647 || cur > physGB*1024*0.95) {
			F = append(F, finding("MEM-01", "warning", "Память", "Недостаточный или не заданный резерв памяти ОС",
				[]string{fmt.Sprintf("max server memory = %.0f МБ; физическая память = %.1f ГБ", cur, physGB), "Источник: " + articleDeployment},
				"Неограниченный лимит либо резерв менее 5% — повод проверить вытеснение и конкуренцию служб; max server memory не ограничивает всю память процесса SQL.",
				"Рассчитайте резерв ОС, потоков, других инстансов и сервера 1С по пику потребления. В статье 6 ГБ при 8 ядрах/32 ГБ — пример, не универсальная формула; на больших системах резерв растёт. Проверьте LPIM и резерв памяти гипервизора. Не применяйте фиксированные 75% ко всем серверам.", ""))
		}
	}
	if m := cfg["max degree of parallelism"]; m != nil {
		if cur, ok := gf(m, "value_in_use"); ok && (cur == 0 || cur == 1 || (cpuCount > 0 && cur > float64(cpuCount))) {
			F = append(F, finding("DOP-01", "info", "Параллелизм", "MAXDOP: проверьте соответствие профилю нагрузки",
				[]string{fmt.Sprintf("MAXDOP = %.0f; логических CPU = %d", cur, cpuCount), "Профиль: " + workloadName(d.Workload), "Источник: " + articleOperation},
				"0 не ограничивает параллелизм настройкой, 1 отключает его; ни одно значение само по себе не доказывает проблему.", workloadAdvice(d.Workload)+parallelAdvice, ""))
		}
	}
	if m := cfg["cost threshold for parallelism"]; m != nil {
		if cur, ok := gf(m, "value_in_use"); ok && cur <= 5 {
			F = append(F, finding("DOP-03", "info", "Параллелизм", "Низкий cost threshold: проверьте планы запросов",
				[]string{fmt.Sprintf("cost threshold = %.0f", cur), "Источник: " + articleOperation},
				"Низкий порог может допускать параллельные планы лёгких запросов; это не измеренное доказательство избыточного параллелизма.", workloadAdvice(d.Workload)+parallelAdvice, ""))
		}
	}

	// ---------- 4. кэш планов ----------
	if m := cfg["optimize for ad hoc workloads"]; m != nil {
		cur, _ := gf(m, "value_in_use")
		adhocCnt := 0.0
		for _, r := range d.PlanCache {
			if gs(r, "objtype") == "Adhoc" {
				adhocCnt, _ = gf(r, "cnt")
			}
		}
		if cur == 0 {
			id, sev := "CACHE-02", "info"
			ev := []string{fmt.Sprintf("Adhoc-планов в кэше: %d", int(adhocCnt))}
			if adhocCnt > 2000 {
				id, sev = "CACHE-01", "warning"
				ev = append(ev, "Отключите лишние одноразовые планы, включив параметр")
			}
			F = append(F, finding(id, sev, "План кэша",
				"optimize for ad hoc workloads выключен"+
					func() string {
						if adhocCnt > 2000 {
							return ", в кэше много ad-hoc планов"
						}
						return ""
					}(),
				ev,
				"Ad-hoc запросы (без параметризации) засоряют кэш планов и отнимают память "+
					"у буферного пула.",
				"Включите optimize for ad hoc workloads — в кэше будет храниться только "+
					"«заглушка» плана при первом выполнении.",
				"EXEC sys.sp_configure N'show advanced options', 1; RECONFIGURE;\n"+
					"EXEC sys.sp_configure N'optimize for ad hoc workloads', 1; RECONFIGURE;"))
		}
	}

	// ---------- 5. опасные параметры ----------
	danger := []struct{ name, sev, why, act string }{
		{"priority boost", "warning",
			"SQL Server работает с повышенным приоритетом процесса — может «заморозить» ОС.",
			"Выключите параметр: EXEC sys.sp_configure N'priority boost', 0; RECONFIGURE;"},
		{"lightweight pooling", "warning",
			"Fiber-режим включён — Microsoft не рекомендует, вызывает проблемы с " +
				"планированием и синхронизацией.",
			"Выключите: EXEC sys.sp_configure N'lightweight pooling', 0; RECONFIGURE;"},
		{"xp_cmdshell", "info",
			"xp_cmdshell включён — расширяет поверхность атаки.",
			"Если не используется — выключите: EXEC sys.sp_configure N'xp_cmdshell', 0; " +
				"RECONFIGURE;"},
		{"clr enabled", "info",
			"CLR включён; с SQL 2017 используйте STRICT mode.",
			"Проверьте, что используются только доверенные сборки."},
	}
	for _, x := range danger {
		if m := cfg[x.name]; m != nil {
			cur, _ := gf(m, "value_in_use")
			if cur == 1 {
				sql := ""
				if i := strings.Index(x.act, ": "); i > 0 {
					sql = strings.TrimSpace(x.act[i+2:])
				}
				F = append(F, finding("CFG-"+strings.ToUpper(strings.ReplaceAll(
					strings.TrimSpace(x.name[:6]), " ", "")), x.sev, "Конфигурация",
					fmt.Sprintf("Параметр «%s» включён", x.name),
					[]string{fmt.Sprintf("Текущее значение: %s", gs(m, "value_in_use"))},
					x.why, x.act, sql))
			}
		}
	}

	// ---------- 6. signal wait ----------
	if d.WaitStats != nil && d.WaitStats.TotalMS > 0 {
		signalShare := d.WaitStats.SignalMS / d.WaitStats.TotalMS
		if signalShare > 0.2 {
			F = append(F, finding("WAIT-SIGNAL", "warning", "CPU",
				fmt.Sprintf("Высокий signal wait: %.0f%% времени ожидания", signalShare*100),
				[]string{fmt.Sprintf("Signal wait: %s из %s суммарных ожиданий",
					fmtMSdot(d.WaitStats.SignalMS), fmtMSdot(d.WaitStats.TotalMS))},
				"Signal wait — время готовности запроса к запуску в очереди планировщика. "+
					"Доля выше 20% — классический признак нехватки процессорных мощностей.",
				"Проверьте загрузку CPU, уменьшите нагрузку от тяжёлых запросов, проверьте "+
					"MAXDOP и статистику.", ""))
		}
	}

	// ---------- 7. доминирующие ожидания ----------
	if d.WaitStats != nil && len(d.WaitStats.Rows) > 0 {
		cats := waitCategoryTotals(d.WaitStats.Rows)
		total := 0.0
		for _, v := range cats {
			total += v
		}
		type catKV struct {
			k string
			v float64
		}
		var list []catKV
		for k, v := range cats {
			list = append(list, catKV{k, v})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
		if total > 0 {
			for _, kv := range list {
				share := kv.v / total
				if share < 0.15 || kv.v < 60000 {
					continue
				}
				sharePct := share * 100
				var top []string
				for i, r := range d.WaitStats.Rows {
					if i >= 5 {
						break
					}
					if waitInfo(r.Type).Cat == kv.k {
						top = append(top, fmt.Sprintf("%s (%s)", r.Type, fmtMSdot(r.WaitMS)))
					}
				}
				topStr := "Топ: " + strings.Join(top, ", ")
				switch kv.k {
				case "io":
					F = append(F, finding("WAIT-IO", "warning", "Ввод-вывод",
						fmt.Sprintf("Ожидания диска доминируют: %.0f%% (%s)", sharePct,
							fmtMSdot(kv.v)),
						[]string{topStr},
						"Данные часто читаются с диска: либо буферному пулу не хватает "+
							"памяти, либо подсистема хранения медленная, либо запросы "+
							"сканируют лишние данные.",
						"1) Проверьте PLE и память. 2) Проверьте задержки диска (раздел "+
							"«Диски и ввод-вывод»). 3) Оптимизируйте топ-запросы по чтениям "+
							"и создайте недостающие индексы.", ""))
				case "lock":
					F = append(F, finding("WAIT-LOCK", "warning", "Блокировки",
						fmt.Sprintf("Ожидания блокировок: %.0f%% (%s)", sharePct,
							fmtMSdot(kv.v)),
						[]string{topStr},
						"Транзакции ждут друг друга — пользовательские запросы «тормозят» "+
							"из-за конкуренции за строки/таблицы.",
						rcsiAdvice+" Проверьте блокирующие цепочки, индексы и длительность транзакций.", ""))
				case "cpu":
					if !has("WAIT-CPU") {
						F = append(F, finding("WAIT-CPU", "warning", "CPU",
							fmt.Sprintf("Ожидания CPU/параллелизма: %.0f%% (%s)", sharePct,
								fmtMSdot(kv.v)),
							[]string{topStr},
							"Сервер проводит много времени в ожидании процессора или "+
								"координации параллельных планов.",
							"Настройте MAXDOP/cost threshold, оптимизируйте самые дорогие "+
								"по CPU запросы (раздел «Топ запросов»), проверьте "+
								"статистику.", ""))
					}
				case "memory":
					F = append(F, finding("WAIT-MEM", "warning", "Память",
						fmt.Sprintf("Ожидания грантов памяти: %.0f%% (%s)", sharePct,
							fmtMSdot(kv.v)),
						[]string{"Основные типы: " + strings.Join(topNames(d.WaitStats.Rows,
							"memory", 5), ", ")},
						"Запросам не хватает памяти для сортировок и хеширования — они "+
							"стоят в очереди RESOURCE_SEMAPHORE.",
						"Проверьте max server memory, обновите статистику (ошибки "+
							"кардинальности раздувают гранты), оптимизируйте запросы с "+
							"сортировками.", ""))
				case "network":
					F = append(F, finding("WAIT-NET", "info", "Клиентские приложения",
						fmt.Sprintf("Ожидания на стороне клиента: %.0f%% (%s)", sharePct,
							fmtMSdot(kv.v)),
						[]string{"Основной тип: ASYNC_NETWORK_IO"},
						"Сервер сформировал результат, но приложения медленно читают данные.",
						"Найдите запросы, возвращающие огромные выборки; добавьте "+
							"пагинацию/TOP; проверьте сеть и обработку на клиенте.", ""))
				case "log":
					F = append(F, finding("WAIT-LOG", "warning", "Журнал транзакций",
						fmt.Sprintf("Ожидания записи в журнал: %.0f%% (%s)", sharePct,
							fmtMSdot(kv.v)),
						[]string{"Основной тип: WRITELOG"},
						"Диск под файлами журнала (.ldf) не справляется с потоком транзакций.",
						"Разместите .ldf на быстром SSD/NVMe, проверьте задержки записи, "+
							"проверьте частоту коммитов в приложении.", ""))
				}
				break
			}
		}
	}

	// ---------- 8. давление на память по таймлайну ----------
	var ples []float64
	var lazy []float64
	for i := range samples {
		if samples[i].PLE != nil {
			ples = append(ples, float64(*samples[i].PLE))
		}
		if samples[i].LazyWrites != nil {
			lazy = append(lazy, *samples[i].LazyWrites)
		}
	}
	if len(ples) > 0 {
		mn := ples[0]
		below := 0
		for _, p := range ples {
			if p < mn {
				mn = p
			}
			if p < 300 {
				below++
			}
		}
		if below >= max(3, len(ples)/10) {
			F = append(F, finding("MEM-PRESS", "info", "Память",
				fmt.Sprintf("Page life expectancy регулярно ниже 300 с (минимум %.0f)", mn),
				[]string{fmt.Sprintf("Точек с PLE<300: %d из %d", below, len(ples))},
				"300 с — диагностическая эвристика, не универсальный норматив PLE. Одного PLE недостаточно для вывода о нехватке памяти.",
				"Сравните с эталонным периодом той же системы и NUMA-узлов; сопоставьте падение PLE с lazy writes, физическими чтениями и PAGEIOLATCH. Сначала проверьте планы/сканы, лимит памяти и ballooning, затем решайте вопрос расширения ОЗУ.", ""))
		}
	}
	if len(lazy) > 0 {
		sum := 0.0
		for _, l := range lazy {
			sum += l
		}
		if sum/float64(len(lazy)) > 20 {
			F = append(F, finding("MEM-LAZY", "warning", "Память",
				fmt.Sprintf("Lazy writer работает постоянно (в среднем %.0f стр/с)",
					sum/float64(len(lazy))),
				[]string{"Это означает активное вытеснение страниц из памяти"},
				"Буферный пул переполнен — SQL Server вынужден постоянно сбрасывать "+
					"страницы на диск.",
				"Увеличьте память для SQL Server или снизьте объём чтений.", ""))
		}
	}

	// ---------- 9. блокировки ----------
	if len(blocking) > 0 {
		sev := "warning"
		if len(blocking) > 50 {
			sev = "critical"
		}
		chains := map[string]map[int]bool{}
		for _, e := range blocking {
			k := fmt.Sprint(e.BlockingSessionID)
			if chains[k] == nil {
				chains[k] = map[int]bool{}
			}
			chains[k][e.SessionID] = true
		}
		worstKey, worstCnt := "—", 0
		for k, v := range chains {
			if len(v) > worstCnt {
				worstKey, worstCnt = k, len(v)
			}
		}
		F = append(F, finding("BLK-01", sev, "Блокировки",
			fmt.Sprintf("Зафиксированы блокировки: %d эпизодов за период", len(blocking)),
			[]string{fmt.Sprintf("Уникальных «блокирующих» сессий: %d", len(chains)),
				fmt.Sprintf("Худшая: сессия %s блокировала %d других сессий", worstKey, worstCnt)},
			"Одни сессии удерживали блокировки, другие ждали. Это прямая причина "+
				"«зависаний» запросов у пользователей.",
			"Найдите виновника в разделе активных сессий, сократите транзакции и проверьте индексы. "+rcsiAdvice, ""))
	} else if d.Live != nil && len(d.Live.Blockers) > 0 {
		var ids []string
		for _, b := range d.Live.Blockers {
			ids = append(ids, fmt.Sprint(gii(b, "session_id")))
		}
		F = append(F, finding("BLK-02", "warning", "Блокировки",
			fmt.Sprintf("Прямо сейчас есть блокировки (%d сессий-виновников)",
				len(d.Live.Blockers)),
			[]string{"Сессии: " + strings.Join(ids, ", ")},
			"На момент снятия диагностики часть запросов была заблокирована.",
			"Изучите запросы виновников в разделе «Активные сессии и блокировки».", ""))
	}

	// ---------- 10. взаимоблокировки ----------
	if len(d.Deadlocks) > 0 {
		var recent []DeadlockInfo
		for _, dl := range d.Deadlocks {
			if dl.Time == nil || timeSince(*dl.Time) <= 48*3600 {
				recent = append(recent, dl)
			}
		}
		if len(recent) > 0 {
			objs := map[string]int{}
			for _, dl := range recent {
				for _, o := range dl.Objects {
					objs[o]++
				}
			}
			var objList []string
			for o, n := range objs {
				objList = append(objList, fmt.Sprintf("%s (%d)", o, n))
			}
			sev := "warning"
			if len(recent) > 5 {
				sev = "critical"
			}
			F = append(F, finding("DL-01", sev, "Взаимоблокировки",
				fmt.Sprintf("Взаимоблокировки за последние 48 ч: %d", len(recent)),
				[]string{"Затронутые объекты: " + strings.Join(objList, ", ")},
				"Две сессии блокируют друг друга, одна откатывается. Чаще всего причина — "+
					"разный порядок доступа к таблицам или отсутствие подходящих индексов.",
				"1) Выровняйте порядок доступа к таблицам в приложении. 2) Проверьте "+
					"индексы по условиям конфликтных запросов. "+rcsiAdvice, ""))
		}
	}

	// ---------- 11. отсутствующие индексы ----------
	var goodIdx []map[string]any
	for _, r := range d.MissingIdx {
		if gfi(r, "user_seeks") >= 100 && gfi(r, "avg_user_impact") >= 60 {
			goodIdx = append(goodIdx, r)
		}
	}
	if len(goodIdx) > 0 {
		var ev []string
		for i, r := range goodIdx {
			if i >= 4 {
				break
			}
			ev = append(ev, fmt.Sprintf("Например: %s (seeks: %d, ожидаемый эффект: %.0f%%)",
				lastPart(gs(r, "obj"), "."), int(gfi(r, "user_seeks")), gfi(r, "avg_user_impact")))
		}
		F = append(F, finding("IDX-01", "warning", "Индексы",
			fmt.Sprintf("Отсутствующие индексы: %d рекомендаций с высокой эффективностью",
				len(goodIdx)),
			ev,
			"Оптимизатор сообщил бы о значительном ускорении, если бы существовали "+
				"перечисленные индексы. Это одна из самых частых причин медленных запросов.",
			"Создайте индексы по готовым DDL из раздела «Индексы и статистика» (проверяйте "+
				"на тестовой среде; учитывайте накладные расходы на INSERT/UPDATE).",
			gs(goodIdx[0], "ddl")))
	}

	// ---------- 12. фрагментация ----------
	var fragBad, fragMid []map[string]any
	for _, r := range d.Frag {
		f := gfi(r, "frag")
		if f >= 30 {
			fragBad = append(fragBad, r)
		} else if f >= 10 {
			fragMid = append(fragMid, r)
		}
	}
	if len(fragBad) > 0 {
		var ev []string
		for i, r := range fragBad {
			if i >= 4 {
				break
			}
			ev = append(ev, fmt.Sprintf("Топ: %s — %.0f%%", gs(r, "ix_name"), gfi(r, "frag")))
		}
		sev := "warning"
		if len(fragBad) <= 5 {
			sev = "info"
		}
		F = append(F, finding("IDX-02", sev, "Индексы",
			fmt.Sprintf("Сильная фрагментация: %d индексов (≥30%%)", len(fragBad)),
			ev,
			"Логическое чтение фрагментированных индексов медленнее, больше упреждающих "+
				"чтений и памяти.",
			"Перестройте (REBUILD) индексы с фрагментацией ≥30%, при 10–30% — "+
				"реорганизуйте (REORGANIZE). Полные DDL — в разделе «Индексы и статистика».",
			"-- Пример:\nALTER INDEX [имя] ON [схема].[таблица] REBUILD;"))
	}
	if len(fragMid) > 0 {
		F = append(F, finding("IDX-03", "info", "Индексы",
			fmt.Sprintf("Умеренная фрагментация: %d индексов (10–30%%)", len(fragMid)),
			[]string{"Эти индексы достаточно реорганизовать (REORGANIZE), без REBUILD"},
			"Умеренная фрагментация слегка снижает производительность чтений.",
			"ALTER INDEX [имя] ON [схема].[таблица] REORGANIZE;", ""))
	}

	// ---------- 13. устаревшая статистика ----------
	var stale []map[string]any
	for _, r := range d.Stats {
		rowsN := gfi(r, "rows")
		mods := gfi(r, "modification_counter")
		if h, ok := hoursAgo(r, "last_updated"); ok {
			if rowsN > 10000 && (mods > 0.25*rowsN || (h > 336 && mods > 0.05*rowsN)) {
				stale = append(stale, r)
			}
		}
	}
	if len(stale) > 0 {
		sort.SliceStable(stale, func(i, j int) bool {
			return gfi(stale[i], "modification_counter") > gfi(stale[j], "modification_counter")
		})
		var ev []string
		for i, r := range stale {
			if i >= 4 {
				break
			}
			ev = append(ev, fmt.Sprintf("Топ: %s.%s.%s — изменено %s строк из %s, "+
				"обновлялась %s", lastPart(gs(r, "db"), "."), gs(r, "tbl"), gs(r, "stat_name"),
				fmtInt(gfi(r, "modification_counter")), fmtInt(gfi(r, "rows")),
				strHead(gs(r, "last_updated"), 19)))
		}
		F = append(F, finding("STAT-01", "warning", "Статистика",
			fmt.Sprintf("Устаревшая статистика: %d объектов", len(stale)),
			ev,
			"Устаревшая статистика даёт оптимизатору неверные оценки кардинальности — "+
				"выбираются плохие планы, растут гранты памяти и CPU. Типичная причина "+
				"«внезапных» замедлений запроса.",
			"Обновите статистику по проблемным таблицам (или по всей базе в окно "+
				"обслуживания).",
			"UPDATE STATISTICS [схема].[таблица] WITH FULLSCAN; -- по таблице\n"+
				"EXEC sp_updatestats; -- вся база (быстрее, менее точно)"))
	}

	// ---------- 14. неиспользуемые индексы ----------
	if len(d.UnusedIdx) > 0 {
		totMB := 0.0
		for _, r := range d.UnusedIdx {
			totMB += gfi(r, "size_mb")
		}
		sorted := append([]map[string]any(nil), d.UnusedIdx...)
		sort.SliceStable(sorted, func(i, j int) bool {
			return gfi(sorted[i], "size_mb") > gfi(sorted[j], "size_mb")
		})
		var ev []string
		for i, r := range sorted {
			if i >= 4 {
				break
			}
			ev = append(ev, fmt.Sprintf("Топ по объёму: %s.%s — %.0f МБ, %s изменений",
				gs(r, "tbl"), gs(r, "ix_name"), gfi(r, "size_mb"),
				fmtInt(gfi(r, "updates"))))
		}
		F = append(F, finding("IDX-04", "info", "Индексы",
			fmt.Sprintf("Неиспользуемые индексы: %d шт (%.0f МБ)", len(d.UnusedIdx), totMB),
			ev,
			"Индексы не используются для чтения, но каждое изменение таблицы обновляет их — "+
				"лишние записи, память и журнал.",
			"Счётчики сбрасываются при перезапуске сервера — сначала убедитесь, что прошёл "+
				"репрезентативный период (несколько недель). Затем отключите (DISABLE) "+
				"ненужные индексы и наблюдайте.",
			"ALTER INDEX [имя] ON [схема].[таблица] DISABLE; -- отключить\n"+
				"DROP INDEX [имя] ON [схема].[таблица]; -- удалить"))
	}

	// ---------- 15. планы: конвертации, проливы, дубли ----------
	if d.PlanIssues != nil {
		if len(d.PlanIssues.Converts) > 0 {
			var ev []string
			for i, c := range d.PlanIssues.Converts {
				if i >= 3 {
					break
				}
				ev = append(ev, fmt.Sprintf("Пример: %s (%s)", c.Issue, strHead(c.Expr, 90)))
			}
			F = append(F, finding("PLAN-CONV", "warning", "Планы запросов",
				fmt.Sprintf("Неявные преобразования типов в %d топовых планах",
					len(d.PlanIssues.Converts)),
				ev,
				"CONVERT в предикатах мешает использованию индексов (index seek превращается "+
					"в scan) и искажает оценки. Классика: сравнение NVARCHAR-параметра с "+
					"VARCHAR-колонкой.",
				"Приведите типы параметров к типам колонок (или наоборот), проверьте "+
					"сопоставление (collation) и типы в ORM/клиенте.", ""))
		}
		if len(d.PlanIssues.Spills) > 0 {
			F = append(F, finding("PLAN-SPILL", "warning", "Планы запросов",
				fmt.Sprintf("Проливы в tempdb в %d планах (SpillToTempDb)",
					len(d.PlanIssues.Spills)),
				[]string{"Пролив означает, что памяти на сортировку/хеш не хватило, и " +
					"данные ушли в tempdb"},
				"Сортировки и хеширования выполняются в tempdb вместо памяти — это на "+
					"порядок медленнее.",
				"Обновите статистику, проверьте запрос (лишние сортировки), проверьте "+
					"память сервера.", ""))
		}
	}
	if d.QStats != nil && len(d.QStats.DupPlans) > 0 {
		var ev []string
		for i, r := range d.QStats.DupPlans {
			if i >= 3 {
				break
			}
			ev = append(ev, fmt.Sprintf("%s разных планов для запроса: %s...",
				fmtInt(gfi(r, "plan_count")), strHead(gs(r, "sql_text"), 60)))
		}
		F = append(F, finding("PLAN-DUP", "info", "Планы запросов",
			"Много планов для одного запроса (возможен parameter sniffing)",
			ev,
			"Один и тот же запрос (один query_hash) имеет множество планов — обычно это "+
				"«разнюхивание параметров» или непараметризованные значения.",
			"Включите Query Store, найдите регрессии планов; рассмотрите OPTION "+
				"(RECOMPILE) или OPTIMIZE FOR для проблемных запросов.", ""))
	}

	// ---------- 16. долгие запросы ----------
	if len(longq) > 0 {
		top := append([]LongQuery(nil), longq...)
		sort.Slice(top, func(i, j int) bool { return top[i].ElapsedS > top[j].ElapsedS })
		var ev []string
		for i, q := range top {
			if i >= 4 {
				break
			}
			ev = append(ev, fmt.Sprintf("Топ: сессия %d — %.0f c: %s",
				q.SessionID, q.ElapsedS, strHead(q.TextHead, 70)))
		}
		F = append(F, finding("Q-LONG", "info", "Запросы",
			fmt.Sprintf("Долгие запросы за период: %d (дольше порога)", len(longq)),
			ev,
			"Эти запросы работали дольше всех; подробные тексты — в разделе «Долгие "+
				"запросы за период мониторинга».",
			"Проверьте их планы (SET STATISTICS IO, TIME ON; актуальный план выполнения), "+
				"индексы и статистику.", ""))
	}

	// ---------- 17. базы данных ----------
	if d.DBs != nil {
		var compat, verify, ash, ac, vlf, logfull []map[string]any
		for _, dbm := range d.DBs.Databases {
			if gii(dbm, "is_system") == 1 {
				continue
			}
			if v, ok := gi(dbm, "compatibility_level"); ok && v < 130 {
				compat = append(compat, dbm)
			}
			if v := gs(dbm, "page_verify_option_desc"); v != "" && v != "CHECKSUM" {
				verify = append(verify, dbm)
			}
			if gii(dbm, "is_auto_shrink_on") == 1 {
				ash = append(ash, dbm)
			}
			if gii(dbm, "is_auto_close_on") == 1 {
				ac = append(ac, dbm)
			}
			if v, ok := gf(dbm, "vlf"); ok && v > 1000 {
				vlf = append(vlf, dbm)
			}
			if p, ok := gf(dbm, "log_used_pct"); ok && p > 80 &&
				gs(dbm, "recovery_model_desc") == "FULL" {
				logfull = append(logfull, dbm)
			}
		}
		names := func(list []map[string]any, n int) string {
			var out []string
			for i, r := range list {
				if i >= n {
					break
				}
				out = append(out, gs(r, "name"))
			}
			return strings.Join(out, ", ")
		}
		if len(compat) > 0 {
			major := 0
			if v, ok := gf(d.Server, "major"); ok {
				major = int(v)
			}
			recCL := 130
			if major > 0 {
				recCL = max(major*10, 130)
				if recCL > 160 {
					recCL = 160
				}
			}
			F = append(F, finding("DB-COMPAT", "info", "Базы данных",
				fmt.Sprintf("Устаревший уровень совместимости: %d баз", len(compat)),
				[]string{"Базы: " + func() string {
					var out []string
					for i, r := range compat {
						if i >= 6 {
							break
						}
						out = append(out, fmt.Sprintf("%s (%d)", gs(r, "name"),
							gii(r, "compatibility_level")))
					}
					return strings.Join(out, ", ")
				}()},
				"Старый уровень совместимости отключает новый оптимизатор кардинальности "+
					"(CE) и новые оптимизации.",
				"Повысьте уровень до версии сервера после тестирования (разница CE может "+
					"менять планы).",
				fmt.Sprintf("ALTER DATABASE [%s] SET COMPATIBILITY_LEVEL = %d;",
					gs(compat[0], "name"), recCL)))
		}
		if len(verify) > 0 {
			F = append(F, finding("DB-VERIFY", "warning", "Базы данных",
				fmt.Sprintf("CHECKSUM выключен в %d базах", len(verify)),
				[]string{"Базы: " + names(verify, 6)},
				"Без CHECKSUM повреждённые страницы не выявляются при чтении/бэкапе.",
				fmt.Sprintf("ALTER DATABASE [%s] SET PAGE_VERIFY CHECKSUM;",
					gs(verify[0], "name")),
				fmt.Sprintf("ALTER DATABASE [%s] SET PAGE_VERIFY CHECKSUM;",
					gs(verify[0], "name"))))
		}
		if len(ash) > 0 {
			F = append(F, finding("DB-SHRINK", "warning", "Базы данных",
				"AUTO_SHRINK включён: "+names(ash, 5),
				[]string{"AUTO_SHRINK постоянно уменьшает файлы и тут же они растут снова"},
				"Постоянные сжатия/росты фрагментируют файлы и нагружают диск.",
				fmt.Sprintf("ALTER DATABASE [%s] SET AUTO_SHRINK OFF;", gs(ash[0], "name")),
				fmt.Sprintf("ALTER DATABASE [%s] SET AUTO_SHRINK OFF;", gs(ash[0], "name"))))
		}
		if len(ac) > 0 {
			F = append(F, finding("DB-CLOSE", "warning", "Базы данных",
				"AUTO_CLOSE включён: "+names(ac, 5),
				[]string{"База закрывается, когда нет подключений, и открывается заново " +
					"при каждом подключении"},
				"Каждое новое подключение оплачивает запуск базы — лишние задержки.",
				fmt.Sprintf("ALTER DATABASE [%s] SET AUTO_CLOSE OFF;", gs(ac[0], "name")),
				fmt.Sprintf("ALTER DATABASE [%s] SET AUTO_CLOSE OFF;", gs(ac[0], "name"))))
		}
		if len(vlf) > 0 {
			var ev []string
			for _, r := range vlf[:min(5, len(vlf))] {
				ev = append(ev, fmt.Sprintf("%s (%d)", gs(r, "name"), gii(r, "vlf")))
			}
			F = append(F, finding("DB-VLF", "warning", "Базы данных",
				"Слишком много VLF в журнале: "+strings.Join(ev, ", "),
				[]string{"Виртуальных файлов журнала (VLF) больше 1000"},
				"Тысячи VLF замедляют восстановление базы, операции с журналом и бэкапы.",
				"Сожмите журнал до минимума и создайте заново одним приращением (в окно "+
					"обслуживания).",
				fmt.Sprintf("USE [%s];\nDBCC SHRINKFILE (лог_имя, 64);\n"+
					"ALTER DATABASE [%s] MODIFY FILE ( NAME = лог_имя, SIZE = 8GB );",
					gs(vlf[0], "name"), gs(vlf[0], "name"))))
		}
		for _, dbm := range logfull[:min(3, len(logfull))] {
			p, _ := gf(dbm, "log_used_pct")
			F = append(F, finding("DB-LOG", "warning", "Журнал транзакций",
				fmt.Sprintf("Журнал базы %s заполнен на %.0f%%", gs(dbm, "name"), p),
				[]string{fmt.Sprintf("recovery_model: FULL, log_reuse_wait: %s",
					gs(dbm, "log_reuse_wait_desc"))},
				"Файл журнала почти заполнен. При исчерпании места запись в базу остановится.",
				"Проверьте регулярность бэкапов журнала (если FULL), размер и автоприращение "+
					"файла .ldf.", ""))
		}
		for _, dbm := range d.DBs.Databases {
			if gs(dbm, "recovery_model_desc") != "FULL" || gii(dbm, "is_system") == 1 {
				continue
			}
			for _, b := range d.Backups {
				if gs(b, "db") != gs(dbm, "name") {
					continue
				}
				if _, hasLog := gtime(b, "last_log"); !hasLog {
					if _, hasFull := gtime(b, "last_full"); hasFull {
						F = append(F, finding("DB-LOGBACKUP", "warning",
							"Резервное копирование",
							fmt.Sprintf("База %s: модель FULL, но бэкапы журнала не делаются",
								gs(dbm, "name")),
							[]string{"Последняя полная копия: " +
								strHead(gs(b, "last_full"), 19)},
							"Без бэкапов журнала файл .ldf растёт бесконечно и не усекается.",
							"Проверьте доступную историю msdb, внешнее резервирование и расписание бэкапов журнала по RPO. Не переключайте FULL в SIMPLE и не делайте аварийный shrink вместо устранения причины.", ""))
					}
				}
				break
			}
		}
	}

	// ---------- 18. бэкапы ----------
	var oldBackups []map[string]any
	for _, b := range d.Backups {
		if h, ok := b["last_full_ago_h"].(float64); ok {
			if h > 168 {
				oldBackups = append(oldBackups, b)
			}
		} else if !hasTime(b, "last_full") {
			oldBackups = append(oldBackups, b)
		}
	}
	if len(oldBackups) > 0 {
		var names []string
		for i, b := range oldBackups {
			if i >= 6 {
				break
			}
			names = append(names, gs(b, "db"))
		}
		sev := "warning"
		if len(oldBackups) >= 2 {
			sev = "critical"
		}
		F = append(F, finding("BK-01", sev, "Резервное копирование",
			fmt.Sprintf("Нет свежих полных бэкапов: %d баз", len(oldBackups)),
			[]string{"Базы: " + strings.Join(names, ", ")},
			"Риск полной потери данных при сбое диска. Это самый приоритетный пункт.",
			"Настройте регулярные полные (ежедневно/еженедельно) + разностные или "+
				"журнальные бэкапы.", ""))
	}

	// ---------- 19. задания ----------
	if len(d.JobsFailed) > 0 {
		var ev []string
		for i, j := range d.JobsFailed {
			if i >= 3 {
				break
			}
			ev = append(ev, fmt.Sprintf("Пример: %s — %s", gs(j, "job"),
				strHead(gs(j, "message_head"), 80)))
		}
		F = append(F, finding("JOB-01", "warning", "Обслуживание",
			fmt.Sprintf("Неудачные задания SQL Agent: %d за 2 дня", len(d.JobsFailed)),
			ev,
			"Часть задач обслуживания/интеграции падает. Среди них могут быть бэкапы и "+
				"обслуживание индексов — это влияет на производительность.",
			"Проверьте историю заданий в SSMS (SQL Server Agent → Jobs) и устраните "+
				"причины сбоев.", ""))
	}

	// ---------- 20. tempdb ----------
	if d.Tempdb != nil && cpuCount > 0 {
		dataFiles := 0
		for _, f := range d.Tempdb.Files {
			if gs(f, "type_desc") == "ROWS" {
				dataFiles++
			}
		}
		if dataFiles > 0 && dataFiles < min(8, cpuCount) {
			var names []string
			for i, f := range d.Tempdb.Files {
				if i >= 8 {
					break
				}
				names = append(names, gs(f, "name"))
			}
			F = append(F, finding("TEMP-01", "info", "tempdb",
				fmt.Sprintf("Файлов данных tempdb: %d (ядер: %d)", dataFiles, cpuCount),
				[]string{"Файлы: " + strings.Join(names, ", ")},
				"Это меньше стартового ориентира min(8, CPU), но ещё не доказательство конфликтов. Проверьте PAGELATCH и страницы распределения tempdb.",
				"Создайте по одному файлу данных на ядро (до 8) одинакового размера с "+
					"одинаковым автоприращением.",
				""))
		}
		if verMB := gfi(d.Tempdb.Space, "version_mb"); verMB > 1024 && len(d.Tempdb.SnapshotTR) > 0 {
			tr := d.Tempdb.SnapshotTR[0]
			F = append(F, finding("TEMP-02", "warning", "tempdb",
				fmt.Sprintf("Version store в tempdb: %.0f МБ", verMB),
				[]string{fmt.Sprintf("Старейшая транзакция снимков: сессия %d, %.0f с",
					gii(tr, "session_id"), gfi(tr, "elapsed_time_seconds"))},
				"Длинные транзакции в базах с изоляцией снимков удерживают версионные "+
					"данные в tempdb, раздувая её.",
				"Найдите и сократите длинные транзакции с уровнем изоляции SNAPSHOT/RCSI.",
				""))
		}
	}

	// ---------- 21. журнал ошибок ----------
	io833 := 0
	io833Ex := ""
	for _, e := range d.ErrorLog {
		text := gs(e, "text")
		if strings.Contains(text, "taking longer than") {
			if io833 == 0 {
				io833Ex = strHead(text, 140)
			}
			io833++
		}
	}
	if io833 > 0 {
		F = append(F, finding("ELOG-833", "warning", "Диски",
			fmt.Sprintf("Предупреждения о медленном вводе-выводе (833): %d", io833),
			[]string{"Пример: " + io833Ex},
			"Диски не возвращают результаты операций за 15 секунд. Замедляются все "+
				"операции с базой.",
			"Проверьте подсистему хранения (нагрузка SAN, кэш контроллера, очередь), "+
				"разнесите файлы данных/журнала по разным томам.", ""))
	}
	growCnt, memCnt := 0, 0
	var growEx, memEx string
	for _, e := range d.ErrorLog {
		text := gs(e, "text")
		if strings.Contains(strings.ToLower(text), "autogrow") && growCnt == 0 {
			growEx = strHead(text, 140)
		}
		if strings.Contains(strings.ToLower(text), "autogrow") {
			growCnt++
		}
		lt := strings.ToLower(text)
		if strings.Contains(lt, "memory") && strings.Contains(lt, "not enough") && memEx == "" {
			memEx = strHead(text, 140)
		}
		if strings.Contains(lt, "memory") && strings.Contains(lt, "not enough") {
			memCnt++
		}
	}
	if growCnt > 0 {
		F = append(F, finding("ELOG-GROW", "info", "Файлы",
			fmt.Sprintf("События автоприращения файлов: %d", growCnt),
			[]string{"Пример: " + growEx},
			"Частые автоприращения останавливают запросы на время роста файла.",
			"Задайте файлам достаточный размер заранее и фиксированный шаг приращения "+
				"(в МБ, не в %).", ""))
	}
	if memCnt > 0 {
		F = append(F, finding("ELOG-MEM", "critical", "Память",
			fmt.Sprintf("Ошибки нехватки памяти в журнале: %d", memCnt),
			[]string{"Пример: " + memEx},
			"SQL Server испытывал дефицит памяти для внутренних операций — возможны сбои "+
				"запросов.",
			"Проверьте max server memory, потребление памяти другими процессами, "+
				"рассмотрите увеличение ОЗУ.", ""))
	}

	// ---------- 22. медленные файлы ----------
	var slowFiles []map[string]any
	for _, r := range d.IOFiles {
		rd := gfi(r, "num_of_reads")
		wr := gfi(r, "num_of_writes")
		rl, wl := 0.0, 0.0
		if rd > 100 {
			rl = gfi(r, "io_stall_read_ms") / rd
		}
		if wr > 100 {
			wl = gfi(r, "io_stall_write_ms") / wr
		}
		if rl > 25 || wl > 40 {
			r["avg_rd_ms"], r["avg_wr_ms"] = rl, wl
			slowFiles = append(slowFiles, r)
		}
	}
	if len(slowFiles) > 0 {
		sort.SliceStable(slowFiles, func(i, j int) bool {
			return max(gfi(slowFiles[i], "avg_rd_ms"), gfi(slowFiles[i], "avg_wr_ms")) >
				max(gfi(slowFiles[j], "avg_rd_ms"), gfi(slowFiles[j], "avg_wr_ms"))
		})
		var ev []string
		for i, s := range slowFiles {
			if i >= 5 {
				break
			}
			ev = append(ev, fmt.Sprintf("%s: чтение %.0f мс, запись %.0f мс",
				firstNonEmpty(gs(s, "logical_name"), gs(s, "db")),
				gfi(s, "avg_rd_ms"), gfi(s, "avg_wr_ms")))
		}
		F = append(F, finding("IO-SLOW", "warning", "Диски",
			fmt.Sprintf("Медленные файлы базы: %d (чтение >25 мс / запись >40 мс)",
				len(slowFiles)),
			ev,
			"Подсистема хранения не обеспечивает типовые задержки (норма: чтение <10–20 "+
				"мс, запись <10–20 мс). Все ожидания ввода-вывода усиливаются.",
			"Проверьте настройки SAN/контроллера, очереди дисков, перенесите «горячие» "+
				"файлы на SSD, разнесите данные и журнал по разным томам.", ""))
	}

	// ---------- 23. место на томах ----------
	var lowVols []map[string]any
	for _, v := range d.Volumes {
		total := gfi(v, "total_gb")
		if total > 0 && gfi(v, "free_gb") < 0.1*total {
			lowVols = append(lowVols, v)
		}
	}
	if len(lowVols) > 0 {
		var ev []string
		for i, v := range lowVols {
			if i >= 5 {
				break
			}
			ev = append(ev, fmt.Sprintf("%s: свободно %.1f ГБ из %.1f ГБ",
				gs(v, "volume_mount_point"), gfi(v, "free_gb"), gfi(v, "total_gb")))
		}
		F = append(F, finding("VOL-FREE", "warning", "Диски",
			fmt.Sprintf("Мало свободного места: %d томов (<10%%)", len(lowVols)),
			ev,
			"При заполнении тома файлы баз перестанут расти — запись остановится.",
			"Освободите место или расширьте томы; проверьте, что бэкапы не хранятся на "+
				"томах баз.", ""))
	}

	// ---------- 24. автоприращение ----------
	if d.DBs != nil {
		var pct, tiny []map[string]any
		for _, f := range d.DBs.Files {
			if gii(f, "is_percent_growth") == 1 {
				pct = append(pct, f)
			} else if gfi(f, "growth") > 0 && gfi(f, "growth")*8/1024 < 64 {
				tiny = append(tiny, f)
			}
		}
		if len(pct) > 0 {
			var ev []string
			for i, f := range pct {
				if i >= 4 {
					break
				}
				ev = append(ev, fmt.Sprintf("Пример: %s (%s) — рост %s%%",
					gs(f, "logical_name"), gs(f, "db"), gs(f, "growth")))
			}
			F = append(F, finding("FILE-GROW", "info", "Файлы",
				fmt.Sprintf("Автоприращение в процентах: %d файлов", len(pct)),
				ev,
				"Процентный рост непредсказуем: большие файлы растут гигабайтами (долгие "+
					"паузы), маленькие — слишком часто.",
				"Установите фиксированный прирост (например, 256–512 МБ для данных, "+
					"512–1024 МБ для журнала).", ""))
		}
		if len(tiny) > 0 {
			var ev []string
			for i, f := range tiny {
				if i >= 4 {
					break
				}
				ev = append(ev, fmt.Sprintf("Пример: %s (%s) — шаг %s×8КБ",
					gs(f, "logical_name"), gs(f, "db"), gs(f, "growth")))
			}
			F = append(F, finding("FILE-TINY", "info", "Файлы",
				fmt.Sprintf("Очень маленький шаг автоприращения: %d файлов (<64 МБ)", len(tiny)),
				ev,
				"Файл растёт слишком часто — каждая операция роста останавливает запись.",
				"Увеличьте шаг приращения до 256–512 МБ.", ""))
		}
	}

	// Query Store state, including READ_ONLY, is checked by BuildAudit.

	// ---------- 26. компиляции ----------
	var comps, batchs []float64
	for i := range samples {
		if samples[i].CompilesSec != nil {
			comps = append(comps, *samples[i].CompilesSec)
		}
		if samples[i].BatchReq != nil {
			batchs = append(batchs, *samples[i].BatchReq)
		}
	}
	if len(comps) > 0 && len(batchs) > 0 {
		avgC, avgB := avg(comps), avg(batchs)
		if avgB > 50 && avgC/max(avgB, 1) > 0.15 {
			F = append(F, finding("COMPILE-01", "warning", "Компиляция",
				fmt.Sprintf("Высокая доля компиляций: %.0f/с при %.0f запросах/с",
					avgC, avgB),
				[]string{"Каждый 6-й и более запрос компилируется заново"},
				"Компиляция требует CPU и блокирует кэш планов — признак непараметризованных "+
					"запросов.",
				"Проверьте параметризацию в приложении; включите optimize for ad hoc "+
					"workloads; для типовых нагрузок рассмотрите принудительную "+
					"параметризацию.", ""))
		}
	}

	// ---------- 27. аптайм ----------
	if st, ok := gtime(d.SysInfo, "sqlserver_start_time"); ok {
		h := timeSince(st) / 3600
		if h < 72 {
			F = append(F, finding("UP-01", "info", "Обслуживание",
				fmt.Sprintf("Сервер перезапускался %.0f ч назад", h),
				[]string{"Статистика ожиданий и счётчики индексов накоплены с момента запуска"},
				"Часть данных (wait stats, usage индексов) ещё не репрезентативна.",
				"Повторите диагностику через несколько дней нормальной работы.", ""))
		}
	}

	// No automatic RCSI prescription from instance-wide waits: affected databases
	// and the application isolation mode have to be verified first.
	F = append(F, auditFindings(d)...)
	for i := range F {
		if strings.HasPrefix(F[i].ID, "WAIT-") {
			scope := "Дельты ожиданий за наблюдаемый период"
			if !measured {
				scope = "Накопленные ожидания с перезапуска: не доказательство текущей проблемы"
				F[i].Severity = "info"
			}
			F[i].Evidence = append(F[i].Evidence, scope, "Источник: "+articleOperation)
			F[i].Action += " Сопоставьте динамику с жалобами и эталонным спокойным периодом; ожидания сами по себе не ошибки."
		}
	}

	sort.SliceStable(F, func(i, j int) bool {
		return sevOrder[F[i].Severity] < sevOrder[F[j].Severity]
	})
	for i := range F {
		F[i].Num = i + 1
	}
	return F
}

func topNames(rows []WaitRow, cat string, n int) []string {
	var out []string
	for i, r := range rows {
		if i >= 5 {
			break
		}
		if waitInfo(r.Type).Cat == cat {
			out = append(out, r.Type)
		}
	}
	return out
}

// fmtMSdot — как analysis._fmt_ms в Python: десятичная точка.
func fmtMSdot(ms float64) string {
	switch {
	case ms >= 3600000:
		return fmt.Sprintf("%.1f ч", ms/3600000.0)
	case ms >= 60000:
		return fmt.Sprintf("%.1f мин", ms/60000.0)
	default:
		return fmt.Sprintf("%.0f с", ms/1000.0)
	}
}

func fmtInt(v float64) string {
	n := int64(v)
	s := fmt.Sprintf("%d", n)
	if len(s) > 3 {
		var parts []string
		for len(s) > 3 {
			parts = append([]string{s[len(s)-3:]}, parts...)
			s = s[:len(s)-3]
		}
		parts = append([]string{s}, parts...)
		s = strings.Join(parts, " ")
	}
	return s
}

func timeSince(t time.Time) float64 { return time.Since(t).Seconds() }

func avg(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func hasTime(m map[string]any, k string) bool {
	_, ok := gtime(m, k)
	return ok
}
