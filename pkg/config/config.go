// Package config provides configuration utilities for CrowdLlama.
package config

import (
	"flag"
	"fmt"
	"strings"

	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// WorkerCfg contains worker-specific configuration
type WorkerCfg struct {
	OllamaBaseURL string // Base URL for Ollama API endpoint (e.g., "http://localhost:11434")
}

// ConsumerCfg contains consumer-specific configuration
type ConsumerCfg struct {
	// Consumer-specific fields can be added here in the future
}

// Configuration is the main configuration structure that embeds worker and consumer configs
type Configuration struct {
	Verbose        bool
	KeyPath        string // Path to the private key file
	Logger         *zap.Logger
	OllamaBaseURL  string
	BootstrapPeers []string
	WorkerCfg
	ConsumerCfg
}

// NewConfiguration creates a new configuration with default values
func NewConfiguration() *Configuration {
	return &Configuration{
		Verbose:        false,
		KeyPath:        "",                       // Will be set to default if not provided
		OllamaBaseURL:  "http://localhost:11434", // Default Ollama base URL
		BootstrapPeers: nil,
	}
}

// ParseFlags parses command line flags and updates the configuration
func (cfg *Configuration) ParseFlags(flagSet *flag.FlagSet) {
	flagSet.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "Enable verbose logging")
	flagSet.StringVar(&cfg.KeyPath, "key", cfg.KeyPath, "Path to private key file (default: ~/.crowdllama/<component>.key)")
	flagSet.StringVar(
		&cfg.OllamaBaseURL,
		"ollama-url",
		cfg.OllamaBaseURL,
		"Base URL for Ollama API endpoint (e.g., http://localhost:11434)",
	)
}

// LoadFromEnvironment loads configuration from environment variables
func (cfg *Configuration) LoadFromEnvironment() {
	// Reset viper to ensure clean state
	viper.Reset()

	// Set up viper to read environment variables
	viper.SetEnvPrefix("CROWDLLAMA")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	// Map environment variables to configuration fields
	if viper.IsSet("VERBOSE") {
		cfg.Verbose = viper.GetBool("VERBOSE")
	}

	if viper.IsSet("KEY_PATH") {
		cfg.KeyPath = viper.GetString("KEY_PATH")
	}

	if viper.IsSet("OLLAMA_URL") {
		cfg.OllamaBaseURL = viper.GetString("OLLAMA_URL")
	}
}

// IsVerbose returns true if verbose logging is enabled
func (cfg *Configuration) IsVerbose() bool {
	return cfg.Verbose
}

// GetOllamaBaseURL returns the configured Ollama base URL
func (cfg *Configuration) GetOllamaBaseURL() string {
	return cfg.OllamaBaseURL
}

// SetupLogger initializes the zap logger based on configuration
func (cfg *Configuration) SetupLogger() error {
	var logger *zap.Logger
	var err error

	if cfg.Verbose {
		// Development logger with debug level and console encoding
		config := zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		logger, err = config.Build()
	} else {
		// Production logger with info level and JSON encoding
		config := zap.NewProductionConfig()
		logger, err = config.Build()
	}

	if err != nil {
		return fmt.Errorf("build zap logger: %w", err)
	}

	cfg.Logger = logger
	return nil
}

// GetLogger returns the configured logger
func (cfg *Configuration) GetLogger() *zap.Logger {
	if cfg.Logger == nil {
		// Fallback to a basic logger if not configured
		logger, _ := zap.NewProduction()
		return logger
	}
	return cfg.Logger
}
