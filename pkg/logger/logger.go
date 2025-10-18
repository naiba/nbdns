package logger

import (
	"log"
	"os"
)

// Logger 定义日志接口
type Logger interface {
	Printf(format string, v ...interface{})
	Println(v ...interface{})
}

// DebugLogger 实现 Logger 接口，支持调试模式
type DebugLogger struct {
	Debug bool
}

// New 创建新的日志实例
func New(debug bool) Logger {
	if !debug {
		log.SetOutput(os.Stdout)
	}
	return &DebugLogger{Debug: debug}
}

func (l *DebugLogger) Printf(format string, v ...interface{}) {
	if l.Debug {
		log.Printf(format, v...)
	}
}

func (l *DebugLogger) Println(v ...interface{}) {
	if l.Debug {
		log.Println(v...)
	}
}
