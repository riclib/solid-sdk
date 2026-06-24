package log

// Config holds configuration for all loggers.
type Config struct {
	Dir  string       `mapstructure:"dir"`
	App  LoggerConfig `mapstructure:"app"`
	NATS LoggerConfig `mapstructure:"nats"`
	HTTP LoggerConfig `mapstructure:"http"`
}

// LoggerConfig holds configuration for a single logger.
type LoggerConfig struct {
	Level  string `mapstructure:"level"`  // debug, info, warn, error
	Format string `mapstructure:"format"` // console, json, text
}

// DefaultConfig returns sensible defaults for logging.
func DefaultConfig() Config {
	return Config{
		Dir: "./log/v3",
		App: LoggerConfig{
			Level:  "info",
			Format: "console",
		},
		NATS: LoggerConfig{
			Level:  "info",
			Format: "text",
		},
		HTTP: LoggerConfig{
			Level:  "info",
			Format: "text",
		},
	}
}
