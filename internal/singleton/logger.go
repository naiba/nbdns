package singleton

import (
	"log"
	"os"
)

type _Logger struct {
	Debug bool
}

var Logger *_Logger

func InitLogger(debug bool) {
	Logger = &_Logger{Debug: debug}
	if !debug {
		log.SetOutput(os.Stdout)
	}
}

func (l *_Logger) Printf(format string, v ...interface{}) {
	if l.Debug {
		log.Printf(format, v...)
	}
}

func (l *_Logger) Println(v ...interface{}) {
	if l.Debug {
		log.Println(v...)
	}
}
