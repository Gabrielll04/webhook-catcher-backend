package logging

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

type Logger struct {
	mu sync.Mutex
}

func New() *Logger {
	log.SetOutput(os.Stdout)
	return &Logger{}
}

func (l *Logger) Info(msg string, fields map[string]any) {
	l.log("info", msg, fields)
}

func (l *Logger) Error(msg string, fields map[string]any) {
	l.log("error", msg, fields)
}

func (l *Logger) log(level, msg string, fields map[string]any) {
	rec := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"level": level,
		"msg":   msg,
	}
	for k, v := range fields {
		rec[k] = v
	}
	b, err := json.Marshal(rec)
	if err != nil {
		log.Printf(`{"ts":"%s","level":"error","msg":"logger_marshal_failed","err":%q}`,
			time.Now().UTC().Format(time.RFC3339Nano), err.Error())
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	log.Print(string(b))
}
