package logger

import (
	"bytes"
	"fmt"

	"github.com/lanthora/cacao/argp"
	"github.com/sirupsen/logrus"
)

func init() {
	logger = logrus.New()
	logger.SetReportCaller(true)
	logger.SetFormatter(&logFormatter{})

	// An unknown level keeps the logrus default (info), as before.
	_ = SetLevel(argp.Get("loglevel", "info"))

	Info("loglevel=[%v]", logger.GetLevel().String())
}

// SetLevel applies a config log level. Invalid levels leave the current level
// untouched and are reported to the caller.
func SetLevel(level string) error {
	switch level {
	case "debug":
		logger.SetLevel(logrus.DebugLevel)
	case "info":
		logger.SetLevel(logrus.InfoLevel)
	default:
		return fmt.Errorf("unknown loglevel %q (want \"debug\" or \"info\")", level)
	}
	return nil
}

var logger *logrus.Logger

type logFormatter struct{}

func (f *logFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	b := &bytes.Buffer{}
	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf("[%s] [%s] %s\n", timestamp, entry.Level, entry.Message)
	b.WriteString(msg)
	return b.Bytes(), nil
}

func Fatal(format string, args ...interface{}) {
	logger.Fatalf(format, args...)
}

func Info(format string, args ...interface{}) {
	logger.Infof(format, args...)
}

func Debug(format string, args ...interface{}) {
	logger.Debugf(format, args...)
}

func Warn(format string, args ...interface{}) {
	logger.Warnf(format, args...)
}
