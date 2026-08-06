package logger

import (
	"io"
	"os"
)

// EnvLevel is the environment variable that sets the log level.
const EnvLevel = "LOG_LEVEL"

// Options configures the logger. Level is the only knob and can be supplied
// from a config file (yaml/json) or the environment; an empty or unrecognized
// value resolves to info.
//
// Output defaults to os.Stdout and exists for tests and alternate sinks; it is
// intentionally not serialized.
type Options struct {
	Level  string    `yaml:"level" json:"level"`
	Output io.Writer `yaml:"-" json:"-"`
}

// FromEnv returns Options with the level read from LOG_LEVEL. An unset variable
// yields the info default.
func FromEnv() Options { return Options{Level: os.Getenv(EnvLevel)} }
