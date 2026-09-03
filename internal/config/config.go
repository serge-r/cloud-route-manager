// Package config loads and validates the YAML configuration of the service.
package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Cloud provider identifiers accepted by general.cloud.
const (
	CloudAuto   = "auto"
	CloudAWS    = "aws"
	CloudYandex = "yandex"
)

// Duration is a time.Duration that unmarshals from strings like "10m".
type Duration time.Duration

// UnmarshalYAML decodes "30s", "10m", "1h" and plain integers (seconds).
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		// Bare numbers are convenient in YAML; treat them as seconds.
		var secs int64
		if _, serr := fmt.Sscanf(raw, "%d", &secs); serr == nil {
			*d = Duration(time.Duration(secs) * time.Second)
			return nil
		}
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	*d = Duration(v)
	return nil
}

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String implements fmt.Stringer.
func (d Duration) String() string { return time.Duration(d).String() }

// Config is the root of the configuration file.
type Config struct {
	General     General     `yaml:"general"`
	Source      Source      `yaml:"source"`
	Destination Destination `yaml:"destination"`
	Actions     Actions     `yaml:"actions"`
}

// General holds service-wide settings. Every key here has a command line
// flag of the same name, which takes precedence when it is given explicitly.
type General struct {
	// Interval between scans. Zero or negative means "run once and exit".
	Interval Duration `yaml:"interval"`
	// LogSeverity is the minimum level to log: debug, info, warn or error.
	LogSeverity string `yaml:"log-severity"`
	// LogFile is "stdout", "stderr" or a path to a file.
	LogFile string `yaml:"log-file"`
	// LogFormat is "text" or "json".
	LogFormat string `yaml:"log-format"`
	// DryRun logs the planned changes without touching the cloud API.
	DryRun bool `yaml:"dry-run"`
	// Cloud forces the provider instead of autodetecting it.
	Cloud string `yaml:"cloud"`
	// Interface overrides the detected primary interface.
	Interface string `yaml:"interface"`
	// IPAddress overrides the detected primary address.
	IPAddress string `yaml:"ip-address"`
	// Timeout bounds a single scan-and-update cycle.
	Timeout Duration `yaml:"timeout"`
	// ActionTimeout bounds a single action command.
	ActionTimeout Duration `yaml:"action-timeout"`
}

// Source lists the route sources. Every configured source is queried on each
// pass and the results are merged.
type Source struct {
	Static         *StaticSource   `yaml:"static"`
	File           *FileSource     `yaml:"file"`
	S3             *S3Source       `yaml:"s3"`
	AWSSSM         *SSMSource      `yaml:"aws-ssm"`
	YandexMetadata *YandexMetaData `yaml:"yandex-instance-metadata"`
}

// StaticSource is a route list embedded into the configuration file.
type StaticSource struct {
	Routes []string `yaml:"routes"`
}

// FileSource reads routes from a local file.
type FileSource struct {
	Path string `yaml:"path"`
	// Optional marks a missing file as an empty list instead of an error.
	Optional bool `yaml:"optional"`
}

// S3Source reads routes from an object in an S3-compatible storage.
type S3Source struct {
	Region string `yaml:"region"`
	Bucket string `yaml:"bucket"`
	Path   string `yaml:"path"`
	// Endpoint targets a non-AWS S3 implementation, e.g. Yandex Object
	// Storage at https://storage.yandexcloud.net.
	Endpoint string `yaml:"endpoint"`
	// UsePathStyle is required by most S3-compatible implementations.
	UsePathStyle bool `yaml:"use-path-style"`
}

// SSMSource reads routes from an AWS SSM parameter.
type SSMSource struct {
	Region string `yaml:"region"`
	Path   string `yaml:"path"`
}

// YandexMetaData reads routes from an instance metadata attribute.
type YandexMetaData struct {
	Key string `yaml:"key"`
}

// Destination lists the cloud route tables to maintain.
type Destination struct {
	RouteTableIDs []string `yaml:"route-table-ids"`
}

// Actions are shell commands executed after a cycle. ${routes},
// ${ip-address}, ${interface} and ${cloud} are substituted before execution.
type Actions struct {
	Success []string `yaml:"success"`
	Failed  []string `yaml:"failed"`
	// RunOnStart fires the success actions after the first successful cycle
	// even when the route tables were already up to date.
	RunOnStart bool `yaml:"run-on-start"`
}

// GeneralKeys lists the YAML keys of the general section, in declaration
// order. The command line mirrors this list one flag per key.
func GeneralKeys() []string {
	t := reflect.TypeFor[General]()
	keys := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if tag != "" && tag != "-" {
			keys = append(keys, tag)
		}
	}
	return keys
}

// Defaults returns a configuration with all optional fields filled in.
func Defaults() Config {
	return Config{
		General: General{
			Interval:      Duration(10 * time.Minute),
			LogSeverity:   "info",
			LogFile:       "stdout",
			LogFormat:     "text",
			Cloud:         CloudAuto,
			Timeout:       Duration(2 * time.Minute),
			ActionTimeout: Duration(1 * time.Minute),
		},
	}
}

// Load reads, parses and validates the configuration file.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse decodes the configuration from YAML bytes.
func Parse(data []byte) (Config, error) {
	cfg := Defaults()
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks the configuration for obviously unusable values.
func (c *Config) Validate() error {
	switch strings.ToLower(c.General.LogSeverity) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("general.log-severity: must be debug, info, warn or error, got %q", c.General.LogSeverity)
	}
	switch strings.ToLower(c.General.LogFormat) {
	case "text", "json":
	default:
		return fmt.Errorf("general.log-format: must be text or json, got %q", c.General.LogFormat)
	}
	switch strings.ToLower(c.General.Cloud) {
	case CloudAuto, CloudAWS, CloudYandex, "":
	default:
		return fmt.Errorf("general.cloud: must be auto, aws or yandex, got %q", c.General.Cloud)
	}
	if !c.Source.configured() {
		return fmt.Errorf("source: at least one source must be configured")
	}
	if c.Source.File != nil && c.Source.File.Path == "" {
		return fmt.Errorf("source.file.path: must not be empty")
	}
	if s := c.Source.S3; s != nil {
		if s.Bucket == "" || s.Path == "" {
			return fmt.Errorf("source.s3: bucket and path are required")
		}
	}
	if s := c.Source.AWSSSM; s != nil && s.Path == "" {
		return fmt.Errorf("source.aws-ssm.path: must not be empty")
	}
	if s := c.Source.YandexMetadata; s != nil && s.Key == "" {
		return fmt.Errorf("source.yandex-instance-metadata.key: must not be empty")
	}
	if len(c.Destination.RouteTableIDs) == 0 {
		return fmt.Errorf("destination.route-table-ids: must not be empty")
	}
	for i, id := range c.Destination.RouteTableIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("destination.route-table-ids[%d]: must not be empty", i)
		}
	}
	return nil
}

func (s Source) configured() bool {
	return s.Static != nil || s.File != nil || s.S3 != nil || s.AWSSSM != nil || s.YandexMetadata != nil
}
