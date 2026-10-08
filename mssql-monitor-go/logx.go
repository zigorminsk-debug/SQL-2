// logx.go — журнал: файл + кольцевой буфер для GUI.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type LogLine struct {
	Seq  int64  `json:"seq"`
	Time string `json:"time"`
	Text string `json:"text"`
}

type Logger struct {
	mu   sync.Mutex
	f    *os.File
	ring []LogLine
	seq  int64
}

func NewLogger() *Logger {
	l := &Logger{}
	os.MkdirAll(LogsDir(), 0o755)
	p := filepath.Join(LogsDir(), "app_"+time.Now().Format("20060102")+".log")
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		l.f = f
	}
	return l
}

func (l *Logger) Log(msg string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	line := LogLine{Seq: l.seq, Time: time.Now().Format("15:04:05"), Text: msg}
	l.ring = append(l.ring, line)
	if len(l.ring) > 600 {
		l.ring = l.ring[len(l.ring)-500:]
	}
	if l.f != nil {
		fmt.Fprintf(l.f, "%s %s\n", line.Time, msg)
	}
}

func (l *Logger) Infof(format string, a ...any) { l.Log(fmt.Sprintf(format, a...)) }

// Since возвращает строки после seq.
func (l *Logger) Since(seq int64) []LogLine {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []LogLine{}
	for _, x := range l.ring {
		if x.Seq > seq {
			out = append(out, x)
		}
	}
	return out
}

func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}
