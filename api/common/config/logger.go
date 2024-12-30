package config

type LogFormat string

type LoggerConfig struct {
	Format        LogFormat `yaml:"format"`
	Level         string    `yaml:"level"`
	Path          string    `yaml:"path"`
	RotationCount uint      `yaml:"rotationCount"`
}
