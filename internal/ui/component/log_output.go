package component

import (
	"fmt"
	"strings"
)

type LogLevel string

const (
	LogDebug LogLevel = "DEBUG"
	LogInfo  LogLevel = "INFO"
	LogWarn  LogLevel = "WARN"
	LogError LogLevel = "ERROR"
)

func LevelFromLine(line string) LogLevel {
	for _, level := range []LogLevel{LogError, LogWarn, LogDebug, LogInfo} {
		if strings.Contains(line, "["+fmt.Sprintf("%-5s", level)+"]") {
			return level
		}
	}
	return LogInfo
}
