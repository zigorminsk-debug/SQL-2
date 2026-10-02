// analysis.go — аналитика: база знаний по ожиданиям, правила рекомендаций,
// поиск «окон замедления», оценка здоровья.
package main

import (
	"fmt"
	"sort"
	"strings"
)

// ===================== база знаний по ожиданиям =====================

type WaitCat struct {
	Title   string
	Desc    string
	Actions []string
}

var waitCategories = map[string]WaitCat{
	"cpu": {"CPU и планировщик",
		"Запросы ждут доступа к процессору или связаны с параллелизмом. Часто означает " +
			"нехватку вычислительных ресурсов либо неоптимальные параллельные планы.",
		[]string{"Проверить загрузку CPU процессом SQL Server и ОС в целом",
			"Настроить MAXDOP и cost threshold for parallelism",
			"Найти самые дорогие запросы по CPU и оптимизировать их"}},
	"lock": {"Блокировки",
		"Ожидание блокировок (LOCK) — одни транзакции мешают другим. Типично для длинных " +
			"транзакций и последовательного изменения «горячих» строк.",
		[]string{"Включить изоляцию моментальных снимков (RCSI) для базы",
			"Сократить длительность транзакций, проверять индексы WHERE/JOIN",
			"Избегать изменения ключей и «горячих» таблиц длинными транзакциями"}},
	"latch": {"Защёлки (latch)",
		"Внутренние ожидания структур данных SQL Server. Высокие значения часто связаны " +
			"с tempdb (PFS/GAM/SGAM) или с большим объёмом модификаций страниц.",
		[]string{"При PAGELATCH на страницах 2:1:1–2:1:128 — увеличить число файлов tempdb",
			"Проверить tempdb и тяжёлые операции сортировок/spool",
			"Проверить память: вытеснение страниц усиливает latch-ожидания"}},
	"io": {"Дисковый ввод-вывод",
		"Ожидание чтения/записи страниц с диска. Либо медленная подсистема, либо нехватка " +
			"памяти буферного пула (данные не помещаются), либо сканирования больших таблиц.",
		[]string{"Проверить задержки диска (менее 10–20 мс на чтение — норма)",
			"Увеличить память / max server memory, проверить PLE",
			"Найти запросы с большими logical/physical reads и оптимизировать",
			"Проверить фрагментацию индексов и статистику"}},
	"network": {"Сеть и клиентские приложения",
		"Сервер отдал данные, но клиент медленно их читает (ASYNC_NETWORK_IO и подобные) — " +
			"узкое место на стороне приложения.",
		[]string{"Проверить, нет ли выбора «всей таблицы» в приложение",
			"Оптимизировать обработку результатов на стороне клиента",
			"Уменьшить объём возвращаемых данных (TOP, пагинация)"}},
	"memory": {"Память (гранты)",
		"Ожидание памяти для выполнения запросов (RESOURCE_SEMAPHORE и др.) — запросам " +
			"не хватает памяти на сортировки/хеширования.",
		[]string{"Проверить max server memory и PLE",
			"Найти запросы с большими грантами памяти и оптимизировать их",
			"Проверить оценку кардинальности/устаревшую статистику"}},
	"log": {"Журнал транзакций",
		"Ожидание записи в журнал транзакций (WRITELOG и др.) — медленный диск под файлы " +
			".ldf или слишком частые мелкие коммиты.",
		[]string{"Разместить .ldf на быстром диске (SSD)",
			"Проверить частоту транзакций, объём журнала и рост файлов",
			"Проверить, делаются ли резервные копии журнала (модель FULL)"}},
	"hadr": {"Высокая доступность / репликация",
		"Ожидания, связанные с AlwaysOn/репликацией (HADR_*), — транзакции ждут " +
			"синхронизации со вторичной репликой.",
		[]string{"Проверить сетевую задержку между репликами",
			"Рассмотреть асинхронную реплику для отчётных нагрузок",
			"Проверить производительность диска журнала на вторичной реплике"}},
	"other": {"Прочее",
		"Ожидания, не попавшие в основные категории.",
		[]string{"Проанализировать динамику и связь с конкретными задачами"}},
}

type waitEntry struct {
	Cat     string
	Title   string
	Desc    string
	Actions []string
}

var waitTypes = map[string]waitEntry{
	"SOS_SCHEDULER_YIELD": {"cpu", "Отказ от кванта процессора",
		"Запросы добровольно уступают процессор — сумма времени ожидания сигнала велика. " +
			"Частый признак нехватки CPU или большого количества мелких запросов.",
		[]string{"Проверить общую загрузку CPU и MAXDOP", "Найти топ запросов по CPU",
			"Проверить статистику и планы (сканирования «съедают» CPU)"}},
	"THREADPOOL": {"cpu", "Нет свободных рабочих потоков",
		"КРИТИЧНО: пул рабочих потоков исчерпан, новые запросы не могут начать выполняться. " +
			"Обычно следствие блокировок или массовых параллельных запросов.",
		[]string{"Проверить блокировки и взаимоблокировки немедленно",
			"Ограничить MAXDOP для крупных запросов", "Проверить max worker threads"}},
	"CXPACKET": {"cpu", "Обмен данными параллельных потоков",
		"Параллельные запросы ждут друг друга. Часть этого ожидания нормальна, но высокая " +
			"доля говорит о неоптимальном параллелизме.",
		[]string{"Проверить cost threshold for parallelism (по умолчанию 5 — часто мало)",
			"Проверить MAXDOP", "Оптимизировать сами запросы, вызывающие параллелизм"}},
	"CXCONSUMER": {"cpu", "Потребление строк параллельными потоками",
		"Сопровождает CXPACKET на новых версиях SQL Server. Оценивается вместе с CXPACKET.",
		[]string{"Как для CXPACKET: cost threshold, MAXDOP, оптимизация запросов"}},
	"RESOURCE_SEMAPHORE": {"memory", "Ожидание гранта памяти",
		"Запросы стоят в очереди за памятью на сортировку/хеширование. Число одновременно " +
			"выполняющихся «тяжёлых» запросов превысило лимит памяти.",
		[]string{"Найти запросы с большими грантами памяти",
			"Проверить статистику (ошибки кардинальности)",
			"Проверить объём памяти сервера"}},
	"RESOURCE_SEMAPHORE_QUERY_COMPILE": {"memory", "Очередь на компиляцию",
		"Не хватает памяти для компиляции запросов — слишком много одновременных компиляций " +
			"или очень сложные запросы.",
		[]string{"Включить optimize for ad hoc workloads",
			"Проверить параметризацию запросов"}},
	"CMEMTHREAD": {"memory", "Доступ к объектам памяти",
		"Конкуренция за внутренние структуры памяти — часто при очень высокой частоте " +
			"мелких операций или неправильной настройке потоков.",
		[]string{"Проверить трассировочные флаги и MAXDOP",
			"Профилировать горячие объекты"}},
	"ASYNC_NETWORK_IO": {"network", "Клиент не забирает данные",
		"SQL Server сформировал результат, но приложение медленно его читает — узкое " +
			"место на стороне приложения или сети.",
		[]string{"Проверить, нет ли SELECT * и выгрузки миллионов строк",
			"Оптимизировать клиентский код (буферизация, пагинация)",
			"Проверить сетевые задержки между приложением и сервером"}},
	"PAGEIOLATCH_SH": {"io", "Чтение страниц с диска (общее)",
		"Запросы читают с диска страницы данных (из-за нехватки памяти в буферном пуле " +
			"или больших сканирований).",
		[]string{"Проверить PLE и объём памяти", "Найти запросы с большими чтениями",
			"Проверить индексы и статистику"}},
	"PAGEIOLATCH_EX": {"io", "Запись страниц на диск (эксклюзивно)",
		"Модифицированные страницы сбрасываются на диск — интенсивная запись данных.",
		[]string{"Проверить задержки записи на диске",
			"Проверить частоту checkpoint и объём изменений"}},
	"PAGEIOLATCH_UP": {"io", "Чтение системных страниц",
		"Обновление системных страниц (PFS/GAM и др.) читается с диска — актуально для " +
			"tempdb и интенсивных вставок.",
		[]string{"Проверить tempdb (число файлов)", "Проверить задержки диска"}},
	"WRITELOG": {"log", "Запись в журнал транзакций",
		"Транзакции ждут записи в лог. Диск под .ldf не успевает или транзакции слишком частые.",
		[]string{"Разместить журнал на быстром SSD/NVMe",
			"Проверить задержку записи (<5–10 мс)",
			"Пересмотреть частоту мелких коммитов"}},
	"LOGMGR_RESERVE_APPEND": {"log", "Нет места в журнале",
		"Журнал транзакций заполнен и не может расти — проверьте рост файла и резервные " +
			"копии журнала.",
		[]string{"Проверить log_reuse_wait и автоприращение .ldf",
			"Настроить регулярные бэкапы журнала (модель FULL)"}},
	"PAGELATCH_EX": {"latch", "Защёлка на изменение страницы",
		"Конкуренция за «горячие» страницы (вставки в таблицу с identity-ключом или tempdb).",
		[]string{"Если ресурс 2:1:* — contention в tempdb: добавить файлы",
			"Проверить «последнюю страницу» при вставках (identity/sequence)"}},
	"PAGELATCH_SH": {"latch", "Защёлка на чтение страницы",
		"Конкуренция за чтение страниц.",
		[]string{"Проверить память и сканирования"}},
	"PAGELATCH_UP": {"latch", "Защёлка на обновление системных страниц",
		"Типично для contention в tempdb (PFS/GAM/SGAM).",
		[]string{"Увеличить число файлов данных tempdb (по числу ядер, до 8)"}},
	"LATCH_EX": {"latch", "Эксклюзивная защёлка",
		"Внутренняя конкуренция при изменении структур в памяти.",
		[]string{"Проверить tempdb и тяжёлые операции DML"}},
	"LCK_M_IS": {"lock", "Блокировка: намерение-разделяемая",
		"Запросы ждут установки блокировки намерения. Смотрите общие рекомендации по " +
			"блокировкам.", []string{"Сократить транзакции", "Включить RCSI"}},
	"LCK_M_S": {"lock", "Блокировка: разделяемая (S)",
		"Читатели ждут завершения пишущих транзакций.",
		[]string{"Сократить транзакции", "Включить RCSI"}},
	"LCK_M_U": {"lock", "Блокировка: обновления (U)",
		"Ожидание блокировки обновления (типично для UPDATE с поиском).",
		[]string{"Проверить индексы по условиям WHERE у UPDATE"}},
	"LCK_M_X": {"lock", "Блокировка: эксклюзивная (X)",
		"Транзакции ждут эксклюзивного доступа — длинные пишущие транзакции блокируют других.",
		[]string{"Сократить длительность транзакций", "Включить RCSI",
			"Проверить, нет ли эскалации блокировок"}},
	"LCK_M_IX": {"lock", "Блокировка: намерение-эксклюзивная",
		"Ожидание блокировки намерения на изменение.",
		[]string{"Сократить транзакции", "Проверить индексы у операций DML"}},
	"LCK_M_SCH_M": {"lock", "Блокировка схемы (изменение)",
		"Ожидание изменения схемы — вероятно, выполняется DDL (ALTER) или перестроение.",
		[]string{"Выполнять обслуживание индексов в окно низкой нагрузки"}},
	"OLEDB": {"network", "Вызовы OLE DB (связанные серверы)",
		"Ожидание ответа от связанных серверов (linked servers) или внешних вызовов.",
		[]string{"Проверить производительность связанных серверов",
			"По возможности вынести запросы к linked servers в автономные задания"}},
	"HADR_SYNC_COMMIT": {"hadr", "Синхронная фиксация на реплику",
		"Каждая транзакция ждёт подтверждения записи на синхронную вторичную реплику AlwaysOn.",
		[]string{"Проверить сеть и диск журнала на вторичной реплике",
			"Перенести отчётную нагрузку на readable secondary",
			"Рассмотреть асинхронную репликацию, если допустимо"}},
	"BACKUPIO": {"io", "Ввод-вывод резервного копирования",
		"Ожидания, связанные с записью бэкапов.",
		[]string{"Проверить скорость устройства резервных копий",
			"Использовать сжатие бэкапов"}},
	"IO_COMPLETION": {"io", "Завершение операций ввода-вывода",
		"Ожидание мелких операций ввода-вывода (не страниц данных).",
		[]string{"Проверить задержки диска, tempdb и сортировки"}},
	"ASYNC_IO_COMPLETION": {"io", "Асинхронный ввод-вывод",
		"Ожидание асинхронных операций (например, рост файлов, бэкапы).",
		[]string{"Проверить автоприращение файлов БД (длительные автогроу)",
			"Выделить достаточный размер файлов заранее"}},
	"MSQL_DQ": {"network", "Распределённые запросы",
		"Ожидание распределённых запросов (linked servers).",
		[]string{"Оптимизировать запросы к linked servers"}},
}

func waitInfo(wt string) waitEntry {
	if e, ok := waitTypes[wt]; ok {
		return e
	}
	catActions := func(cat string) waitEntry {
		return waitEntry{cat, wt, "См. категорию ожиданий.", waitCategories[cat].Actions}
	}
	switch {
	case strings.HasPrefix(wt, "LCK_M_"):
		return waitEntry{"lock", "Блокировки: " + strings.TrimPrefix(wt, "LCK_M_"),
			"Ожидание блокировки уровня " + strings.TrimPrefix(wt, "LCK_M_") + ".",
			waitCategories["lock"].Actions}
	case strings.HasPrefix(wt, "PAGEIOLATCH_"):
		return waitTypes["PAGEIOLATCH_SH"]
	case strings.HasPrefix(wt, "PAGELATCH_"):
		return waitTypes["PAGELATCH_UP"]
	case strings.HasPrefix(wt, "LATCH_"):
		return waitEntry{"latch", wt, "Внутренние защёлки SQL Server.",
			waitCategories["latch"].Actions}
	case strings.HasPrefix(wt, "PREEMPTIVE_"):
		return waitEntry{"other", wt, "Ожидание внешних вызовов ОС (preemptive).",
			[]string{"Проверить внешние операции (linked servers, файловые операции)"}}
	case strings.HasPrefix(wt, "HADR_"):
		return waitTypes["HADR_SYNC_COMMIT"]
	case strings.HasPrefix(wt, "LCK_"):
		return waitEntry{"lock", wt, "Ожидание блокировки.",
			waitCategories["lock"].Actions}
	}
	return catActions("other")
}

func waitCategoryTotals(rows []WaitRow) map[string]float64 {
	totals := map[string]float64{}
	for _, r := range rows {
		cat := waitInfo(r.Type).Cat
		totals[cat] += r.WaitMS
	}
	return totals
}

// ===================== окна замедления =====================

func findSlowdownWindows(samples []Sample, blocking []BlockingEvent,
	longq []LongQuery) []PeakWindow {
	if len(samples) < 10 {
		return nil
	}
	var cpus, batchs, ples []float64
	for _, s := range samples {
		if s.SQLCPU != nil {
			cpus = append(cpus, *s.SQLCPU)
		}
		if s.BatchReq != nil {
			batchs = append(batchs, *s.BatchReq)
		}
		if s.PLE != nil {
			ples = append(ples, float64(*s.PLE))
		}
	}
	sort.Float64s(cpus)
	sort.Float64s(batchs)
	sort.Float64s(ples)
	cpuHi := percentile(cpus, 0.85)
	batchHi := percentile(batchs, 0.85)
	pleLo := percentile(ples, 0.10)

	flags := make([]int, len(samples))
	for i, s := range samples {
		if s.SQLCPU != nil && cpuHi > 0 && *s.SQLCPU >= max(cpuHi, 50) {
			flags[i] |= 1
		}
		if s.BatchReq != nil && batchHi > 0 && *s.BatchReq >= batchHi*1.2 {
			flags[i] |= 1
		}
		if s.Blocked > 0 {
			flags[i] |= 2
		}
		if s.PLE != nil && *s.PLE < 300 && pleLo > 0 {
			flags[i] |= 1
		}
	}
	var windows [][2]int
	i := 0
	n := len(samples)
	for i < n {
		if flags[i] == 0 {
			i++
			continue
		}
		j := i
		gap := 0
		for j+1 < n && gap <= 2 {
			j++
			if flags[j] != 0 {
				gap = 0
			} else {
				gap++
			}
		}
		if gap > 2 && flags[j] == 0 {
			j -= gap
		}
		for j > i && flags[j] == 0 {
			j--
		}
		if j-i >= 2 {
			windows = append(windows, [2]int{i, j})
		}
		i = j + 1
	}

	var out []PeakWindow
	for _, w := range windows {
		seg := samples[w[0] : w[1]+1]
		t0, t1 := seg[0].TS, seg[len(seg)-1].TS
		pw := PeakWindow{T0: t0, T1: t1, N: len(seg), WaitCats: map[string]float64{},
			PLEMin: 1 << 62}
		var cpusSeg []float64
		waits := map[string]float64{}
		batchSum, batchCnt := 0.0, 0
		for idx := range seg {
			s := &seg[idx]
			if s.SQLCPU != nil {
				cpusSeg = append(cpusSeg, *s.SQLCPU)
			}
			if s.BatchReq != nil {
				batchSum += *s.BatchReq
				batchCnt++
			}
			if s.PLE != nil && float64(*s.PLE) < pw.PLEMin {
				pw.PLEMin = float64(*s.PLE)
				pw.PLEMinSet = true
			}
			for _, w := range s.Waits {
				waits[w.Type] += w.WaitMS
			}
		}
		if len(cpusSeg) > 0 {
			pw.CPUMaxSet = true
			sum := 0.0
			for _, c := range cpusSeg {
				sum += c
				if c > pw.CPUMax {
					pw.CPUMax = c
				}
			}
			pw.CPUAvg = sum / float64(len(cpusSeg))
		}
		if batchCnt > 0 {
			pw.BatchAvg = batchSum / float64(batchCnt)
		}
		for wt, ms := range waits {
			pw.Waits = append(pw.Waits, WaitAgg{Type: wt, MS: ms})
			pw.WaitCats[waitInfo(wt).Cat] += ms
		}
		sort.Slice(pw.Waits, func(a, b int) bool { return pw.Waits[a].MS > pw.Waits[b].MS })
		if len(pw.Waits) > 8 {
			pw.Waits = pw.Waits[:8]
		}
		for k := range blocking {
			if blocking[k].TS >= t0 && blocking[k].TS <= t1 {
				pw.Blocked = append(pw.Blocked, blocking[k])
			}
		}
		for k := range longq {
			if longq[k].TS >= t0 && longq[k].TS <= t1 {
				pw.Long = append(pw.Long, longq[k])
			}
		}
		pw.Reasons = windowReasons(pw)
		out = append(out, pw)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if len(out[a].Blocked) != len(out[b].Blocked) {
			return len(out[a].Blocked) > len(out[b].Blocked)
		}
		if out[a].CPUMax != out[b].CPUMax {
			return out[a].CPUMax > out[b].CPUMax
		}
		return out[a].N > out[b].N
	})
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

func windowReasons(w PeakWindow) []string {
	var reasons []string
	total := 0.0
	for _, v := range w.WaitCats {
		total += v
	}
	if total <= 0 {
		total = 1
	}
	cats := make([]string, 0, len(w.WaitCats))
	for c := range w.WaitCats {
		cats = append(cats, c)
	}
	sort.Slice(cats, func(i, j int) bool { return w.WaitCats[cats[i]] > w.WaitCats[cats[j]] })
	for _, cat := range cats[:3] {
		share := w.WaitCats[cat] / total * 100
		if share < 10 {
			continue
		}
		switch cat {
		case "cpu":
			reasons = append(reasons, fmt.Sprintf(
				"Высокая доля ожиданий CPU/параллелизма (%s — %.0f%% времени окна): "+
					"вероятна нехватка процессорных ресурсов или неоптимальные "+
					"параллельные планы.", waitCategories[cat].Title, share))
		case "io":
			reasons = append(reasons, fmt.Sprintf(
				"Доминируют ожидания дискового ввода-вывода (%.0f%%): данные читаются "+
					"с диска — проверьте память (PLE), индексы и быстродействие "+
					"накопителей.", share))
		case "lock":
			reasons = append(reasons, fmt.Sprintf(
				"Значительная доля блокировок (%.0f%%): в этот момент одни транзакции "+
					"ожидали других.", share))
		case "memory":
			reasons = append(reasons, fmt.Sprintf(
				"Ожидания грантов памяти (%.0f%%): запросам не хватало памяти на "+
					"сортировку/хеширование.", share))
		case "network":
			reasons = append(reasons, fmt.Sprintf(
				"Ожидания на клиенте (%.0f%%): приложения медленно забирали данные.", share))
		case "log":
			reasons = append(reasons, fmt.Sprintf(
				"Ожидания записи в журнал транзакций (%.0f%%): диск под .ldf не успевал "+
					"за потоком транзакций.", share))
		case "latch":
			reasons = append(reasons, fmt.Sprintf(
				"Внутренние защёлки (%.0f%%): возможен contention в tempdb или на "+
					"«горячих» страницах.", share))
		case "hadr":
			reasons = append(reasons, fmt.Sprintf(
				"Ожидания репликации (%.0f%%): фиксация транзакций ждала синхронную "+
					"реплику.", share))
		}
	}
	importantWaits := map[string]bool{"SOS_SCHEDULER_YIELD": true, "THREADPOOL": true,
		"CXPACKET": true, "CXCONSUMER": true, "PAGEIOLATCH_SH": true, "WRITELOG": true,
		"ASYNC_NETWORK_IO": true, "RESOURCE_SEMAPHORE": true, "LCK_M_X": true,
		"LCK_M_IX": true, "LCK_M_S": true, "PAGELATCH_UP": true}
	for i, x := range w.Waits {
		if i >= 3 {
			break
		}
		if importantWaits[x.Type] {
			e := waitInfo(x.Type)
			reasons = append(reasons, e.Title+": "+e.Desc)
			break
		}
	}
	if len(w.Blocked) > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"Зафиксировано эпизодов блокировок: %d. Смотрите раздел «Активные сессии и "+
				"блокировки» — там приведены цепочки на этот период.", len(w.Blocked)))
	}
	if len(w.Long) > 0 {
		top := append([]LongQuery(nil), w.Long...)
		sort.Slice(top, func(i, j int) bool { return top[i].ElapsedS > top[j].ElapsedS })
		var parts []string
		for i, q := range top {
			if i >= 3 {
				break
			}
			parts = append(parts, fmt.Sprintf("сессия %d — %.0f с (%s)",
				q.SessionID, q.ElapsedS, strHead(q.TextHead, 60)))
		}
		reasons = append(reasons, "Выполнялись долгие запросы: "+strings.Join(parts, "; "))
	}
	if w.PLEMinSet && w.PLEMin < 300 {
		reasons = append(reasons, fmt.Sprintf(
			"Page life expectancy опускался до %.0f с (<300) — буферному пулу не хватало "+
				"памяти.", w.PLEMin))
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "Повышенная активность без явных ожиданий: вероятно, "+
			"плановая нагрузка (отчёты, задания). Проверьте, какие задания запускались "+
			"в это время.")
	}
	if len(reasons) > 6 {
		reasons = reasons[:6]
	}
	return reasons
}

// ===================== оценка здоровья =====================

func HealthScore(findings []Finding) Health {
	h := Health{}
	for _, f := range findings {
		switch f.Severity {
		case "critical":
			h.Crit++
		case "warning":
			h.Warn++
		case "info":
			h.Info++
		}
	}
	score := 100 - h.Crit*14 - h.Warn*5 - h.Info
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	h.Score = score
	switch {
	case score >= 90:
		h.Grade = "Отличное состояние"
	case score >= 75:
		h.Grade = "Хорошее состояние"
	case score >= 55:
		h.Grade = "Требует внимания"
	case score >= 35:
		h.Grade = "Серьёзные проблемы"
	default:
		h.Grade = "Критическое состояние"
	}
	return h
}

var sevOrder = map[string]int{"critical": 0, "warning": 1, "info": 2}
var sevTitles = map[string]string{"critical": "Критично", "warning": "Важно", "info": "Рекомендация"}
var sevIcons = map[string]string{"critical": "⛔", "warning": "⚠️", "info": "💡"}

func finding(id, sev, cat, title string, evidence []string, why, action, sql string) Finding {
	return Finding{ID: id, Severity: sev, Cat: cat, Title: title,
		Evidence: evidence, Why: why, Action: action, SQLT: sql}
}
