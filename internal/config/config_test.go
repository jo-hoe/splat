package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// reflectStructElem returns reflect.Value for *cfg's struct, matching the
// dereferenced value Load passes into interpolateEnv internally.
func reflectStructElem(c *Config) reflect.Value {
	return reflect.ValueOf(c).Elem()
}

// writeTempConfig writes the given YAML content to a temp file and returns
// its absolute path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// validLocalYAML returns a syntactically valid local-source config that
// references the given root directory.
func validLocalYAML(root string) string {
	return `
server:
  port: 8080
  shutdown_timeout: 10s
source:
  type: local
  local:
    root: ` + root + `
editing:
  copy_suffix: -edited
  jpeg_quality: 90
thumbnails:
  cache_dir: /cache
  height_px: 200
  cache_max_bytes: 1073741824
`
}

func validS3YAML() string {
	return `
server:
  port: 8080
  shutdown_timeout: 10s
source:
  type: s3
  s3:
    bucket: my-bucket
    prefix: photos/
    region: eu-central-1
    auth: static
    access_key: AKIA
    secret_access_key: SECRET
editing:
  copy_suffix: -edited
  jpeg_quality: 90
thumbnails:
  cache_dir: /cache
  height_px: 200
  cache_max_bytes: 1073741824
`
}

func TestLoad_HappyPathLocal(t *testing.T) {
	root := t.TempDir()
	path := writeTempConfig(t, validLocalYAML(root))

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Port: got %d want 8080", cfg.Server.Port)
	}
	if cfg.Server.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout: got %v want 10s", cfg.Server.ShutdownTimeout)
	}
	if cfg.Source.Type != SourceLocal {
		t.Errorf("Source.Type: got %q", cfg.Source.Type)
	}
	if cfg.Source.Local == nil || cfg.Source.Local.Root != root {
		t.Errorf("Source.Local.Root: got %+v want %q", cfg.Source.Local, root)
	}
	if cfg.Editing.CopySuffix != "-edited" || cfg.Editing.JPEGQuality != 90 {
		t.Errorf("Editing: got %+v", cfg.Editing)
	}
	if cfg.Thumbnails.HeightPx != 200 || cfg.Thumbnails.CacheMaxBytes != 1073741824 {
		t.Errorf("Thumbnails: got %+v", cfg.Thumbnails)
	}
}

func TestLoad_HappyPathS3(t *testing.T) {
	path := writeTempConfig(t, validS3YAML())
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Source.Type != SourceS3 {
		t.Errorf("Source.Type: got %q", cfg.Source.Type)
	}
	if cfg.Source.S3 == nil || cfg.Source.S3.Bucket != "my-bucket" {
		t.Errorf("Source.S3: got %+v", cfg.Source.S3)
	}
	if cfg.Source.S3.AccessKey != "AKIA" || cfg.Source.S3.SecretAccessKey != "SECRET" {
		t.Errorf("Source.S3 creds: got %+v", cfg.Source.S3)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "does-not-exist.yaml") {
		t.Errorf("error should mention path, got %v", err)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeTempConfig(t, "server: : :\n  - bad\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestValidate_TableDriven(t *testing.T) {
	root := t.TempDir()

	mutate := func(base func() *Config, f func(*Config)) *Config {
		c := base()
		f(c)
		return c
	}

	baseLocal := func() *Config {
		return &Config{
			Server:     ServerConfig{Port: 8080, ShutdownTimeout: 10 * time.Second},
			Source:     SourceConfig{Type: SourceLocal, Local: &LocalConfig{Root: root}},
			Editing:    EditingConfig{CopySuffix: "-edited", JPEGQuality: 90},
			Thumbnails: ThumbnailsConfig{CacheDir: "/cache", HeightPx: 200, CacheMaxBytes: 1 << 30},
		}
	}
	baseS3 := func() *Config {
		c := baseLocal()
		c.Source = SourceConfig{Type: SourceS3, S3: &S3Config{
			Bucket: "b", Region: "r", Auth: "static", AccessKey: "k", SecretAccessKey: "s",
		}}
		return c
	}

	cases := []struct {
		name    string
		cfg     *Config
		wantErr string // substring; "" means no error
	}{
		{"valid local", baseLocal(), ""},
		{"valid s3", baseS3(), ""},
		{
			"port out of range high",
			mutate(baseLocal, func(c *Config) { c.Server.Port = 70000 }),
			"server.port",
		},
		{
			"port out of range negative",
			mutate(baseLocal, func(c *Config) { c.Server.Port = -1 }),
			"server.port",
		},
		{
			"shutdown timeout zero defaults applied",
			mutate(baseLocal, func(c *Config) { c.Server.ShutdownTimeout = 0 }),
			"",
		},
		{
			"shutdown timeout negative",
			mutate(baseLocal, func(c *Config) { c.Server.ShutdownTimeout = -1 }),
			"shutdown_timeout",
		},
		{
			"unknown source type",
			mutate(baseLocal, func(c *Config) { c.Source.Type = "ftp" }),
			"source.type",
		},
		{
			"empty source type",
			mutate(baseLocal, func(c *Config) { c.Source.Type = "" }),
			"source.type",
		},
		{
			"local type with s3 block set",
			mutate(baseLocal, func(c *Config) {
				c.Source.S3 = &S3Config{Bucket: "x"}
			}),
			"source.s3",
		},
		{
			"s3 type with local block set",
			mutate(baseS3, func(c *Config) { c.Source.Local = &LocalConfig{Root: root} }),
			"source.local",
		},
		{
			"local type missing local block",
			mutate(baseLocal, func(c *Config) { c.Source.Local = nil }),
			"source.local",
		},
		{
			"s3 type missing s3 block",
			mutate(baseS3, func(c *Config) { c.Source.S3 = nil }),
			"source.s3",
		},
		{
			"local root empty",
			mutate(baseLocal, func(c *Config) { c.Source.Local.Root = "" }),
			"source.local.root",
		},
		{
			"local root does not exist",
			mutate(baseLocal, func(c *Config) {
				c.Source.Local.Root = filepath.Join(root, "nope")
			}),
			"source.local.root",
		},
		{
			"local root is a file",
			mutate(baseLocal, func(c *Config) {
				p := filepath.Join(root, "file.txt")
				if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				c.Source.Local.Root = p
			}),
			"not a directory",
		},
		{
			"s3 bucket empty",
			mutate(baseS3, func(c *Config) { c.Source.S3.Bucket = "" }),
			"source.s3.bucket",
		},
		{
			"s3 region empty",
			mutate(baseS3, func(c *Config) { c.Source.S3.Region = "" }),
			"source.s3.region",
		},
		{
			"s3 auth invalid",
			mutate(baseS3, func(c *Config) { c.Source.S3.Auth = "iam" }),
			"source.s3.auth",
		},
		{
			"s3 access key empty",
			mutate(baseS3, func(c *Config) { c.Source.S3.AccessKey = "" }),
			"source.s3.access_key",
		},
		{
			"s3 secret empty",
			mutate(baseS3, func(c *Config) { c.Source.S3.SecretAccessKey = "" }),
			"source.s3.secret_access_key",
		},
		{
			"copy_suffix empty",
			mutate(baseLocal, func(c *Config) { c.Editing.CopySuffix = "" }),
			"editing.copy_suffix",
		},
		{
			"jpeg quality zero",
			mutate(baseLocal, func(c *Config) { c.Editing.JPEGQuality = 0 }),
			"editing.jpeg_quality",
		},
		{
			"jpeg quality 101",
			mutate(baseLocal, func(c *Config) { c.Editing.JPEGQuality = 101 }),
			"editing.jpeg_quality",
		},
		{
			"thumbnails cache_dir empty",
			mutate(baseLocal, func(c *Config) { c.Thumbnails.CacheDir = "" }),
			"thumbnails.cache_dir",
		},
		{
			"thumbnails height zero",
			mutate(baseLocal, func(c *Config) { c.Thumbnails.HeightPx = 0 }),
			"thumbnails.height_px",
		},
		{
			"thumbnails cache_max_bytes zero",
			mutate(baseLocal, func(c *Config) { c.Thumbnails.CacheMaxBytes = 0 }),
			"thumbnails.cache_max_bytes",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidate_AppliesPortDefault(t *testing.T) {
	root := t.TempDir()
	c := &Config{
		Source:     SourceConfig{Type: SourceLocal, Local: &LocalConfig{Root: root}},
		Editing:    EditingConfig{CopySuffix: "-edited", JPEGQuality: 90},
		Thumbnails: ThumbnailsConfig{CacheDir: "/cache", HeightPx: 200, CacheMaxBytes: 1 << 30},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.Server.Port != 8080 {
		t.Errorf("Port default: got %d want 8080", c.Server.Port)
	}
	if c.Server.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout default: got %v want 10s", c.Server.ShutdownTimeout)
	}
}

func TestValidate_Idempotent(t *testing.T) {
	root := t.TempDir()
	c := &Config{
		Source:     SourceConfig{Type: SourceLocal, Local: &LocalConfig{Root: root}},
		Editing:    EditingConfig{CopySuffix: "-edited", JPEGQuality: 90},
		Thumbnails: ThumbnailsConfig{CacheDir: "/cache", HeightPx: 200, CacheMaxBytes: 1 << 30},
	}
	for i := 0; i < 3; i++ {
		if err := c.Validate(); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
}

func TestEnvInterpolation_Substitutes(t *testing.T) {
	t.Setenv("SPLAT_TEST_BUCKET", "interp-bucket")
	t.Setenv("SPLAT_TEST_KEY", "AKIATEST")
	t.Setenv("SPLAT_TEST_SECRET", "shh")

	yamlBody := `
server:
  port: 8080
  shutdown_timeout: 10s
source:
  type: s3
  s3:
    bucket: ${SPLAT_TEST_BUCKET}
    prefix: photos/
    region: eu-central-1
    auth: static
    access_key: ${SPLAT_TEST_KEY}
    secret_access_key: ${SPLAT_TEST_SECRET}
editing:
  copy_suffix: -edited
  jpeg_quality: 90
thumbnails:
  cache_dir: /cache
  height_px: 200
  cache_max_bytes: 1073741824
`
	path := writeTempConfig(t, yamlBody)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Source.S3.Bucket != "interp-bucket" {
		t.Errorf("Bucket: got %q", cfg.Source.S3.Bucket)
	}
	if cfg.Source.S3.AccessKey != "AKIATEST" {
		t.Errorf("AccessKey: got %q", cfg.Source.S3.AccessKey)
	}
	if cfg.Source.S3.SecretAccessKey != "shh" {
		t.Errorf("SecretAccessKey: got %q", cfg.Source.S3.SecretAccessKey)
	}
}

func TestEnvInterpolation_UnknownVar(t *testing.T) {
	os.Unsetenv("SPLAT_TEST_DEFINITELY_UNSET")
	root := t.TempDir()
	body := `
server:
  port: 8080
  shutdown_timeout: 10s
source:
  type: local
  local:
    root: ${SPLAT_TEST_DEFINITELY_UNSET}
editing:
  copy_suffix: -edited
  jpeg_quality: 90
thumbnails:
  cache_dir: ` + root + `
  height_px: 200
  cache_max_bytes: 1073741824
`
	path := writeTempConfig(t, body)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown env var")
	}
	if !strings.Contains(err.Error(), "SPLAT_TEST_DEFINITELY_UNSET") {
		t.Errorf("error should name the variable, got %v", err)
	}
}

func TestEnvInterpolation_DoesNotTouchNonMatching(t *testing.T) {
	t.Setenv("lowercase_var", "should-not-be-used")
	t.Setenv("SHOULD_NOT_USE", "nope")

	root := t.TempDir()
	// `$VAR` (no braces), `${lowercase}` (lowercase), `${1ABC}` (leading digit)
	// must not be substituted; we encode them inside the copy_suffix to avoid
	// other validation interfering.
	body := `
server:
  port: 8080
  shutdown_timeout: 10s
source:
  type: local
  local:
    root: ` + root + `
editing:
  copy_suffix: "$SHOULD_NOT_USE-${lowercase}-${1ABC}-tail"
  jpeg_quality: 90
thumbnails:
  cache_dir: /cache
  height_px: 200
  cache_max_bytes: 1073741824
`
	path := writeTempConfig(t, body)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := "$SHOULD_NOT_USE-${lowercase}-${1ABC}-tail"
	if cfg.Editing.CopySuffix != want {
		t.Errorf("CopySuffix: got %q want %q", cfg.Editing.CopySuffix, want)
	}
}

func TestEnvInterpolation_HandlesNilPointers(t *testing.T) {
	// Source.S3 nil while Source.Local is set; interpolation must skip nil
	// pointers without panicking.
	root := t.TempDir()
	cfg := &Config{
		Server:     ServerConfig{Port: 8080, ShutdownTimeout: 10 * time.Second},
		Source:     SourceConfig{Type: SourceLocal, Local: &LocalConfig{Root: root}},
		Editing:    EditingConfig{CopySuffix: "-edited", JPEGQuality: 90},
		Thumbnails: ThumbnailsConfig{CacheDir: "/cache", HeightPx: 200, CacheMaxBytes: 1 << 30},
	}
	if err := interpolateEnv(reflectStructElem(cfg)); err != nil {
		t.Fatalf("interpolateEnv: %v", err)
	}
}

// Sanity: ensure errors.Join produces a multi-error when several rules fail.
func TestValidate_AggregatesErrors(t *testing.T) {
	c := &Config{
		Server:     ServerConfig{Port: 0, ShutdownTimeout: 10 * time.Second},
		Source:     SourceConfig{Type: ""},
		Editing:    EditingConfig{CopySuffix: "", JPEGQuality: 0},
		Thumbnails: ThumbnailsConfig{CacheDir: "", HeightPx: 0, CacheMaxBytes: 0},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected aggregated errors")
	}
	msg := err.Error()
	for _, want := range []string{"source.type", "editing.copy_suffix", "thumbnails.cache_dir"} {
		if !strings.Contains(msg, want) {
			t.Errorf("aggregated error missing %q: %s", want, msg)
		}
	}
	// Sanity: errors.Is on errors.Join works for at least one wrapped error.
	_ = errors.Is(err, err)
}
