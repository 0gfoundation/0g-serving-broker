package log

import (
	"time"

	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"github.com/rifflock/lfshook"
	log "github.com/sirupsen/logrus"

	"github.com/0glabs/0g-serving-broker/common/config"
)

const (
	JSONLogFormat config.LogFormat = "json"
	TextLogFormat config.LogFormat = "text"
)

// The Logger interface generalizes the Logger types
type Logger interface {
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Printf(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Warningf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
	Fatalf(format string, args ...interface{})
	Panicf(format string, args ...interface{})

	Debug(args ...interface{})
	Info(args ...interface{})
	Print(args ...interface{})
	Warn(args ...interface{})
	Warning(args ...interface{})
	Error(args ...interface{})
	Fatal(args ...interface{})
	Panic(args ...interface{})

	Debugln(args ...interface{})
	Infoln(args ...interface{})
	Println(args ...interface{})
	Warnln(args ...interface{})
	Warningln(args ...interface{})
	Errorln(args ...interface{})
	Fatalln(args ...interface{})
	Panicln(args ...interface{})

	WithFields(fields log.Fields) Logger
	InnerLogger() *log.Logger
}

type logger struct {
	logger *log.Logger
	fields log.Fields
}

// GetLogger returns a logger with the specified configuration.
func GetLogger(cfg *config.LoggerConfig) (Logger, error) {
	l := log.New()

	var formatter log.Formatter
	if cfg.Format == JSONLogFormat {
		formatter = &log.JSONFormatter{}
	} else if cfg.Format == TextLogFormat {
		formatter = &log.TextFormatter{
			DisableColors: true,
			FullTimestamp: true}
	}
	l.SetFormatter(formatter)

	fileLevel, err := log.ParseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	l.SetLevel(fileLevel)

	if cfg.Path != "" {
		hook, err := newLfsHook(cfg.Path, cfg.RotationCount, formatter)
		if err != nil {
			return nil, err
		}

		l.AddHook(hook)
	}

	logger := &logger{logger: l}
	return logger, nil
}

func newLfsHook(logPath string, maxRemainCnt uint, formatter log.Formatter) (log.Hook, error) {
	writer, err := rotatelogs.New(
		logPath+".%Y%m%d%H",
		rotatelogs.WithLinkName(logPath),
		rotatelogs.WithRotationTime(24*time.Hour),
		rotatelogs.WithRotationCount(maxRemainCnt),
	)

	if err != nil {
		return nil, err
	}

	lfsHook := lfshook.NewHook(lfshook.WriterMap{
		log.TraceLevel: writer,
		log.DebugLevel: writer,
		log.InfoLevel:  writer,
		log.WarnLevel:  writer,
		log.ErrorLevel: writer,
		log.FatalLevel: writer,
		log.PanicLevel: writer,
	}, formatter)

	return lfsHook, nil
}

func (l *logger) WithFields(fields log.Fields) Logger {
	return &logger{
		l.logger,
		fields,
	}
}

func (l *logger) InnerLogger() *log.Logger {
	return l.logger
}

func (l *logger) Debugf(format string, args ...interface{}) {
	l.logger.WithFields(l.fields).Debugf(format, args...)
}
func (l *logger) Infof(format string, args ...interface{}) {
	l.logger.WithFields(l.fields).Infof(format, args...)
}
func (l *logger) Printf(format string, args ...interface{}) {
	l.logger.WithFields(l.fields).Printf(format, args...)
}
func (l *logger) Warnf(format string, args ...interface{}) {
	l.logger.WithFields(l.fields).Warnf(format, args...)
}
func (l *logger) Warningf(format string, args ...interface{}) {
	l.logger.WithFields(l.fields).Warningf(format, args...)
}
func (l *logger) Errorf(format string, args ...interface{}) {
	l.logger.WithFields(l.fields).Errorf(format, args...)
}
func (l *logger) Fatalf(format string, args ...interface{}) {
	l.logger.WithFields(l.fields).Fatalf(format, args...)
}
func (l *logger) Panicf(format string, args ...interface{}) {
	l.logger.WithFields(l.fields).Panicf(format, args...)
}

func (l *logger) Debug(args ...interface{}) {
	l.logger.WithFields(l.fields).Debug(args...)
}
func (l *logger) Info(args ...interface{}) {
	l.logger.WithFields(l.fields).Info(args...)
}
func (l *logger) Print(args ...interface{}) {
	l.logger.WithFields(l.fields).Print(args...)
}
func (l *logger) Warn(args ...interface{}) {
	l.logger.WithFields(l.fields).Warn(args...)
}
func (l *logger) Warning(args ...interface{}) {
	l.logger.WithFields(l.fields).Warning(args...)
}
func (l *logger) Error(args ...interface{}) {
	l.logger.WithFields(l.fields).Error(args...)
}
func (l *logger) Fatal(args ...interface{}) {
	l.logger.WithFields(l.fields).Fatal(args...)
}
func (l *logger) Panic(args ...interface{}) {
	l.logger.WithFields(l.fields).Panic(args...)
}

func (l *logger) Debugln(args ...interface{}) {
	l.logger.WithFields(l.fields).Debug(args...)
}
func (l *logger) Infoln(args ...interface{}) {
	l.logger.WithFields(l.fields).Info(args...)
}
func (l *logger) Println(args ...interface{}) {
	l.logger.WithFields(l.fields).Print(args...)
}
func (l *logger) Warnln(args ...interface{}) {
	l.logger.WithFields(l.fields).Warn(args...)
}
func (l *logger) Warningln(args ...interface{}) {
	l.logger.WithFields(l.fields).Warning(args...)
}
func (l *logger) Errorln(args ...interface{}) {
	l.logger.WithFields(l.fields).Error(args...)
}
func (l *logger) Fatalln(args ...interface{}) {
	l.logger.WithFields(l.fields).Fatal(args...)
}
func (l *logger) Panicln(args ...interface{}) {
	l.logger.WithFields(l.fields).Panic(args...)
}
