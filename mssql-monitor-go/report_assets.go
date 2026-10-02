// report_assets.go — CSS и JS отчёта (автогенерировано из Python-версии для полного паритета).
package main

const reportCSS = `
:root{--bg:#eef1f6;--card:#ffffff;--tx:#17202a;--mut:#5b6b7c;--acc:#1a5fb4;
 --crit:#c62828;--warn:#e65100;--info:#0277bd;--ok:#2e7d32;--bd:#dde4ec}
*{box-sizing:border-box}
body{margin:0;font:14px/1.5 "Segoe UI",system-ui,-apple-system,Roboto,Arial,sans-serif;
 background:var(--bg);color:var(--tx)}
main{max-width:1180px;margin:0 auto;padding:14px}
.card{background:var(--card);border:1px solid var(--bd);border-radius:12px;
 padding:18px 20px;margin:14px 0;box-shadow:0 1px 3px rgba(20,40,80,.06)}
.hero{background:linear-gradient(135deg,#123a75,#1a5fb4);color:#fff;border:none}
.hero .h1{font-size:22px;font-weight:700;margin:2px 0 10px}
.hero .brand{opacity:.85;font-size:12px;text-transform:uppercase;letter-spacing:.08em}
.brand{opacity:.85}
.hero-grid{display:flex;gap:26px;flex-wrap:wrap;align-items:center;justify-content:space-between}
.hero .kv td{padding:2px 10px 2px 0;font-size:13px;opacity:.95}
.hero-score{text-align:center;min-width:230px}
.hero .grade{font-size:16px;font-weight:600;margin-top:-6px}
.score-cards{display:flex;gap:8px;justify-content:center;margin-top:10px;flex-wrap:wrap}
.sc{background:rgba(255,255,255,.14);border-radius:8px;padding:2px 8px;font-size:12px}
nav.nav{position:sticky;top:0;z-index:50;background:#ffffffee;backdrop-filter:blur(6px);
 border-bottom:1px solid var(--bd);padding:8px 14px;display:flex;gap:4px;flex-wrap:wrap;
 max-width:1180px;margin:0 auto;border-radius:0 0 10px 10px}
nav.nav a{text-decoration:none;color:var(--acc);padding:3px 9px;border-radius:7px;font-size:13px}
nav.nav a:hover{background:#e8f0fb}
h2{margin:4px 0 12px;font-size:19px;border-left:4px solid var(--acc);padding-left:10px}
h3{font-size:15px;margin:18px 0 8px}
h4{font-size:13px;margin:12px 0 6px;color:var(--mut)}
.lead{font-size:15px}
.muted{color:var(--mut)}
.small{font-size:12px}
table.kv td{padding:2px 12px 2px 0;vertical-align:top}
table.tbl{border-collapse:collapse;width:100%;margin:8px 0;font-size:13px}
table.tbl th{background:#f0f4f9;text-align:left;padding:7px 9px;border:1px solid var(--bd);
 font-weight:600;white-space:nowrap}
table.tbl td{padding:6px 9px;border:1px solid var(--bd);vertical-align:top}
table.tbl tr:nth-child(even) td{background:#fafbfd}
.nowrap{white-space:nowrap}
.tbl code.small, code.small{font:11.5px/1.45 Consolas,"Cascadia Mono",monospace;
 white-space:pre-wrap;word-break:break-word}
.path{font:11.5px Consolas,monospace;color:var(--mut)}
details summary{cursor:pointer;color:var(--acc);white-space:nowrap}
.nodata{color:var(--mut);font-style:italic;padding:10px 0}
.okbox{background:#e8f5e9;border:1px solid #a5d6a7;color:#1b5e20;border-radius:8px;padding:10px 14px}
.warnbox{background:#fff8e1;border:1px solid #ffe082;border-radius:8px;padding:10px 14px;
 margin-top:12px;font-size:13px}
.warnbox ul{margin:6px 0 2px 18px}
.toplist{list-style:none;padding:0;margin:10px 0}
.toplist li{padding:5px 0;border-bottom:1px dashed var(--bd)}
a.sevlink{text-decoration:none;color:var(--tx)}
a.s-critical{color:var(--crit)} a.s-warning{color:var(--warn)} a.s-info{color:var(--info)}
.t-crit{color:var(--crit);font-weight:600}.t-warn{color:var(--warn);font-weight:600}
.t-ok{color:var(--ok)}.t-info{color:var(--info)}
.stats{display:flex;gap:12px;flex-wrap:wrap;margin:10px 0}
.stat{background:#f4f7fb;border:1px solid var(--bd);border-radius:10px;padding:8px 14px;
 min-width:120px;flex:1}
.stat-v{font-size:17px;font-weight:700}
.stat-k{font-size:11.5px;color:var(--mut)}
.chart{width:100%;height:auto;background:#fbfcfe;border:1px solid var(--bd);border-radius:8px}
.chart-block{margin:16px 0}
.grid{stroke:#e3e9f1;stroke-width:1}
.axis{stroke:#9fb0c3;stroke-width:1.2}
.tick{font:11px "Segoe UI",sans-serif;fill:#61707f;text-anchor:end}
text.text-anchor{font:11px "Segoe UI",sans-serif;fill:#61707f}
.unit{font:11px "Segoe UI",sans-serif;fill:#61707f;text-anchor:start}
.donut{width:230px;height:230px}
.donut-c{font:700 16px "Segoe UI",sans-serif;fill:#17202a}
.donut-s{font:11px "Segoe UI",sans-serif;fill:#5b6b7c}
.gauge{width:210px;height:124px}
.gauge-v{font:700 34px "Segoe UI",sans-serif}
.gauge-s{font:12px "Segoe UI",sans-serif;fill:#5b6b7c}
.legend{display:flex;gap:14px;flex-wrap:wrap;margin:6px 0;font-size:12.5px;color:var(--mut)}
.lg{display:inline-flex;align-items:center;gap:5px}
.sw{width:12px;height:12px;border-radius:3px;display:inline-block}
.sw.region{background:repeating-linear-gradient(45deg,#e5393522,#e5393522 4px,#e5393544 4px,#e5393544 8px)}
.hbars{margin:8px 0}
.hbar-row{display:flex;align-items:center;gap:8px;margin:3px 0}
.hbar-l{width:34%;text-align:right;font-size:12px;color:var(--tx);white-space:nowrap;
 overflow:hidden;text-overflow:ellipsis}
.hbar-t{flex:1;background:#eef2f7;border-radius:4px;height:16px;overflow:hidden}
.hbar{height:100%;border-radius:4px}
.hbar-v{width:90px;font-size:12px;color:var(--mut)}
.peak{border:1px solid var(--bd);border-left:5px solid var(--crit);border-radius:10px;
 padding:12px 16px;margin:12px 0;background:#fffafa}
.peak h3{margin:2px 0 6px}
.peak-stats{font-size:13px;color:var(--mut);margin-bottom:6px}
.reasons ul{margin:6px 0 4px 20px}
.reasons li{margin:3px 0}
.waits-grid{display:grid;grid-template-columns:290px 1fr;gap:18px;align-items:start}
@media(max-width:900px){.waits-grid{grid-template-columns:1fr}}
.cats{display:grid;grid-template-columns:repeat(auto-fill,minmax(330px,1fr));gap:12px}
.cat-card{border:1px solid var(--bd);border-radius:10px;padding:10px 14px;background:#fbfcfe}
.cat-h{margin-bottom:4px}
.sev-dot{display:inline-block;width:10px;height:10px;border-radius:50%;margin-right:6px}
.rec{border:1px solid var(--bd);border-radius:10px;padding:12px 16px;margin:10px 0;
 background:#fff}
.r-critical{border-left:6px solid var(--crit)}
.r-warning{border-left:6px solid var(--warn)}
.r-info{border-left:6px solid var(--info)}
.rec-h{display:flex;gap:8px;align-items:baseline;flex-wrap:wrap;margin-bottom:6px}
.badge{color:#fff;border-radius:6px;padding:1px 8px;font-size:11.5px;font-weight:600}
.b-critical{background:var(--crit)}.b-warning{background:var(--warn)}.b-info{background:var(--info)}
.cat{color:var(--mut);font-size:12.5px}
ul.ev{margin:4px 0 8px 20px;color:#37474f;font-size:13px}
.rec-why,.rec-act{margin:6px 0;font-size:13.5px}
.rec-act{background:#f1f8f4;border-radius:8px;padding:8px 12px}
pre.sql{background:#10212f;color:#d7e4f0;border-radius:8px;padding:10px 14px;overflow:auto;
 font:12.5px/1.5 Consolas,"Cascadia Mono",monospace;white-space:pre-wrap}
.filters{display:flex;gap:6px;flex-wrap:wrap;margin:8px 0}
.filters button{border:1px solid var(--bd);background:#fff;border-radius:8px;padding:4px 12px;
 cursor:pointer;font-size:12.5px}
.filters button.on{background:var(--acc);color:#fff;border-color:var(--acc)}
.support-links{margin-top:16px;padding-top:12px;border-top:1px solid var(--bd);color:var(--mut);font-size:12.5px;line-height:1.8}
.support-links a,footer a{color:var(--acc);text-decoration:underline;text-underline-offset:2px}
.support-links a:hover,footer a:hover{color:#124783}
footer{text-align:center;color:var(--mut);font-size:12px;margin:20px 0}
@media print{.no-print{display:none}body{background:#fff}.card{box-shadow:none;
 page-break-inside:avoid}nav.nav{display:none}}
`

const reportJS = `
function filt(sev){
 document.querySelectorAll('#reclist .rec').forEach(function(c){
   c.style.display=(sev==='all'||c.dataset.sev===sev)?'':'none';});
 document.querySelectorAll('.filters button').forEach(function(b){b.classList.remove('on');});
 event.target.classList.add('on');
}
`

// headCSS — каркас <head> отчёта (%TITLE% заменяется на имя сервера).
const headCSS = `<!DOCTYPE html>
<html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Отчёт MS SQL Monitor — %TITLE%</title>
<style>` + reportCSS + `</style></head>
`
