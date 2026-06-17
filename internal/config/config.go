// Package config loads, parses, validates and resolves the splat YAML
// configuration. It performs ${ENV_VAR} interpolation over every string
// field after YAML parsing and before validation.
package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// SourceType is the discriminator for the active source backend.
type SourceType string

// Supported source types.
const (
	SourceLocal SourceType = "local"
	SourceS3    SourceType = "s3"
)

// Config is the root configuration object loaded from YAML.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Source     SourceConfig     `yaml:"source"`
	Editing    EditingConfig    `yaml:"editing"`
	Thumbnails ThumbnailsConfig `yaml:"thumbnails"`
}

// ServerConfig configures the HTTP listener and shutdown behaviour.
type ServerConfig struct {
	Port            int           `yaml:"port"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// SourceConfig selects and configures the active image source.
type SourceConfig struct {
	Type  SourceType   `yaml:"type"`
	Local *LocalConfig `yaml:"local,omitempty"`
	S3    *S3Config    `yaml:"s3,omitempty"`
}

// LocalConfig is the local-filesystem source configuration.
type LocalConfig struct {
	Root string `yaml:"root"`
}

// S3Config is the AWS S3 source configuration.
type S3Config struct {
	Bucket          string `yaml:"bucket"`
	Prefix          string `yaml:"prefix"`
	Region          string `yaml:"region"`
	Auth            string `yaml:"auth"`
	AccessKey       string `yaml:"access_key"`
	SecretAccessKey string `yaml:"secret_access_key"`
}

// EditingConfig configures save / copy semantics for edited images.
type EditingConfig struct {
	CopySuffix  string `yaml:"copy_suffix"`
	JPEGQuality int    `yaml:"jpeg_quality"`
}

// ThumbnailsConfig configures the on-disk thumbnail cache.
type ThumbnailsConfig struct {
	CacheDir      string `yaml:"cache_dir"`
	HeightPx      int    `yaml:"height_px"`
	CacheMaxBytes int64  `yaml:"cache_max_bytes"`
}

// envVarPattern matches ${VAR_NAME} where VAR_NAME starts with an uppercase
// letter or underscore and contains only uppercase letters, digits, or
// underscores.
var envVarPattern = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// Load reads, parses, env-interpolates and validates the YAML config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}

	if err := interpolateEnv(reflect.ValueOf(&cfg).Elem()); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: validate %q: %w", path, err)
	}
	return &cfg, nil
}

// Validate enforces all field-level rules. It is idempotent; missing
// numeric fields receive their documented defaults before validation.
func (c *Config) Validate() error {
	c.applyDefaults()
	return errors.Join(
		c.validateServer(),
		c.validateSource(),
		c.validateEditing(),
		c.validateThumbnails(),
	)
}

func (c *Config) applyDefaults() {
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = 10 * time.Second
	}
}

func (c *Config) validateServer() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port: %d not in [1,65535]", c.Server.Port)
	}
	if c.Server.ShutdownTimeout <= 0 {
		return fmt.Errorf("server.shutdown_timeout: must be > 0")
	}
	return nil
}

func (c *Config) validateSource() error {
	switch c.Source.Type {
	case SourceLocal:
		if c.Source.Local == nil {
			return fmt.Errorf("source.local: must be set when source.type=local")
		}
		if c.Source.S3 != nil {
			return fmt.Errorf("source.s3: must be unset when source.type=local")
		}
		return validateLocal(c.Source.Local)
	case SourceS3:
		if c.Source.S3 == nil {
			return fmt.Errorf("source.s3: must be set when source.type=s3")
		}
		if c.Source.Local != nil {
			return fmt.Errorf("source.local: must be unset when source.type=s3")
		}
		return validateS3(c.Source.S3)
	case "":
		return fmt.Errorf("source.type: required (\"local\" or \"s3\")")
	default:
		return fmt.Errorf("source.type: %q not in {\"local\",\"s3\"}", c.Source.Type)
	}
}

func validateLocal(l *LocalConfig) error {
	if l.Root == "" {
		return fmt.Errorf("source.local.root: required")
	}
	info, err := os.Stat(l.Root)
	if err != nil {
		return fmt.Errorf("source.local.root: stat %q: %w", l.Root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source.local.root: %q is not a directory", l.Root)
	}
	return nil
}

func validateS3(s *S3Config) error {
	if s.Bucket == "" {
		return fmt.Errorf("source.s3.bucket: required")
	}
	if s.Region == "" {
		return fmt.Errorf("source.s3.region: required")
	}
	if s.Auth != "static" {
		return fmt.Errorf("source.s3.auth: %q not supported (only \"static\" in v1)", s.Auth)
	}
	if s.AccessKey == "" {
		return fmt.Errorf("source.s3.access_key: required")
	}
	if s.SecretAccessKey == "" {
		return fmt.Errorf("source.s3.secret_access_key: required")
	}
	return nil
}

func (c *Config) validateEditing() error {
	if c.Editing.CopySuffix == "" {
		return fmt.Errorf("editing.copy_suffix: required (non-empty)")
	}
	if c.Editing.JPEGQuality < 1 || c.Editing.JPEGQuality > 100 {
		return fmt.Errorf("editing.jpeg_quality: %d not in [1,100]", c.Editing.JPEGQuality)
	}
	return nil
}

func (c *Config) validateThumbnails() error {
	if c.Thumbnails.CacheDir == "" {
		return fmt.Errorf("thumbnails.cache_dir: required")
	}
	if c.Thumbnails.HeightPx <= 0 {
		return fmt.Errorf("thumbnails.height_px: must be > 0")
	}
	if c.Thumbnails.CacheMaxBytes <= 0 {
		return fmt.Errorf("thumbnails.cache_max_bytes: must be > 0")
	}
	return nil
}

// interpolateEnv walks v recursively and substitutes ${VAR} patterns in
// every settable string field with os.Getenv. Unknown variables return an
// error naming the variable.
func interpolateEnv(v reflect.Value) error {
	switch v.Kind() {
	case reflect.String:
		return interpolateString(v)
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return interpolateEnv(v.Elem())
	case reflect.Struct:
		return interpolateStruct(v)
	default:
		return nil
	}
}

func interpolateStruct(v reflect.Value) error {
	for i := 0; i < v.NumField(); i++ {
		if err := interpolateEnv(v.Field(i)); err != nil {
			return err
		}
	}
	return nil
}

func interpolateString(v reflect.Value) error {
	if !v.CanSet() {
		return nil
	}
	original := v.String()
	matches := envVarPattern.FindAllStringSubmatchIndex(original, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]byte, 0, len(original))
	cursor := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		nameStart, nameEnd := m[2], m[3]
		name := original[nameStart:nameEnd]
		val, ok := os.LookupEnv(name)
		if !ok {
			return fmt.Errorf("env interpolation: variable %q is not set", name)
		}
		out = append(out, original[cursor:start]...)
		out = append(out, val...)
		cursor = end
	}
	out = append(out, original[cursor:]...)
	v.SetString(string(out))
	return nil
}
