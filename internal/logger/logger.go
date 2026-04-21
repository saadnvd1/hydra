package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Logger struct {
	file *os.File
}

func New(path string) *Logger {
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0755)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// Silently degrade — logging shouldn't block execution
		return &Logger{}
	}
	return &Logger{file: f}
}

func (l *Logger) Log(event string, kvs ...string) {
	if l.file == nil {
		return
	}
	ts := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("[%s] %s", ts, event)
	for i := 0; i+1 < len(kvs); i += 2 {
		line += fmt.Sprintf(" %s=%q", kvs[i], kvs[i+1])
	}
	fmt.Fprintln(l.file, line)
}

func (l *Logger) Close() {
	if l.file != nil {
		l.file.Close()
	}
}
