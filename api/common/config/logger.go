package config

type LogFormat string

type LoggerConfig struct {
	Format        LogFormat `yaml:"format" default:"text"`
	Level         string    `yaml:"level" default:"info"`
	Path          string    `yaml:"path"`
	RotationCount uint      `yaml:"rotationCount" default:"7"`
}
