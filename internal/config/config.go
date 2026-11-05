// Package config loads AgentHub configuration from defaults, a YAML file,
// AGENTHUB_* environment variables and command-line flags (precedence:
// defaults < file < env < flags).
package config

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"go.pilab.hu/agenthub/internal/secrets"
)

// Config is the top-level configuration. Fields map to the reference in
// docs/architecture/operations.md §2.1.
type Config struct {
	Env string `mapstructure:"env"`

	Server        ServerConfig        `mapstructure:"server"`
	Database      DatabaseConfig      `mapstructure:"database"`
	Storage       StorageConfig       `mapstructure:"storage"`
	Cache         CacheConfig         `mapstructure:"cache"`
	Auth          AuthConfig          `mapstructure:"auth"`
	Signing       SigningConfig       `mapstructure:"signing"`
	Federation    FederationConfig    `mapstructure:"federation"`
	Web           WebConfig           `mapstructure:"web"`
	Limits        LimitsConfig        `mapstructure:"limits"`
	Observability ObservabilityConfig `mapstructure:"observability"`
	Features      FeaturesConfig      `mapstructure:"features"`
	Vault         secrets.VaultConfig `mapstructure:"vault"`
}

type ServerConfig struct {
	HTTPAddr        string        `mapstructure:"http_addr"`
	GRPCAddr        string        `mapstructure:"grpc_addr"`
	MetricsAddr     string        `mapstructure:"metrics_addr"`
	PublicURL       string        `mapstructure:"public_url"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	IdleTimeout     time.Duration `mapstructure:"idle_timeout"`
	ShutdownGrace   time.Duration `mapstructure:"shutdown_grace"`
	MaxRequestBytes int64         `mapstructure:"max_request_bytes"`
	// RateLimitPerSec is the per-IP token bucket refill rate (burst equals the
	// rate). Zero or negative disables the limiter.
	RateLimitPerSec int      `mapstructure:"rate_limit_per_sec"`
	TrustedProxies  []string `mapstructure:"trusted_proxies"`
}

type DatabaseConfig struct {
	DSN              string        `mapstructure:"dsn"`
	MaxOpenConns     int           `mapstructure:"max_open_conns"`
	MaxIdleConns     int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime  time.Duration `mapstructure:"conn_max_lifetime"`
	StatementTimeout time.Duration `mapstructure:"statement_timeout"`
	MigrateOnStart   bool          `mapstructure:"migrate_on_start"`
}

// StorageConfig selects the object store. The keys are validated, but the
// server does not construct an S3 backend yet: federation bundle blobs use an
// in-memory store. The fields are reserved for that backend.
type StorageConfig struct {
	Kind                 string        `mapstructure:"kind"`
	Endpoint             string        `mapstructure:"endpoint"`
	Region               string        `mapstructure:"region"`
	BucketBundles        string        `mapstructure:"bucket_bundles"`
	BucketBlobs          string        `mapstructure:"bucket_blobs"`
	BucketExports        string        `mapstructure:"bucket_exports"`
	UsePathStyle         bool          `mapstructure:"use_path_style"`
	PresignTTL           time.Duration `mapstructure:"presign_ttl"`
	ServerSideEncryption string        `mapstructure:"server_side_encryption"`
	MaxBundleBytes       int64         `mapstructure:"max_bundle_bytes"`
	MaxFileBytes         int64         `mapstructure:"max_file_bytes"`
}

// CacheConfig selects the shared cache. The keys are validated, but no server
// component consumes them yet; the fields are reserved for the Valkey backend.
type CacheConfig struct {
	Kind        string        `mapstructure:"kind"`
	Addr        string        `mapstructure:"addr"`
	Password    string        `mapstructure:"password"`
	DB          int           `mapstructure:"db"`
	TTLManifest time.Duration `mapstructure:"ttl_manifest"`
	TTLResolve  time.Duration `mapstructure:"ttl_resolve"`
	TTLSession  time.Duration `mapstructure:"ttl_session"`
}

type AuthConfig struct {
	Issuer            string   `mapstructure:"issuer"`
	DiscoveryURL      string   `mapstructure:"discovery_url"`
	Audience          string   `mapstructure:"audience"`
	AudienceAllowlist []string `mapstructure:"audience_allowlist"`
	JWKSURL           string   `mapstructure:"jwks_url"`
	IntrospectionURL  string   `mapstructure:"introspection_url"`
	ClientID          string   `mapstructure:"client_id"`
	ClientSecret      string   `mapstructure:"client_secret"`
	// SessionSecret keys the HMAC that signs the OIDC BFF login and
	// session cookies (M3.5). Required whenever the browser client
	// (client_id) is configured and must stay stable across restarts,
	// otherwise every session is dropped when the process restarts.
	SessionSecret                 string        `mapstructure:"session_secret"`
	RedirectURL                   string        `mapstructure:"redirect_url"`
	PostLogoutRedirectURL         string        `mapstructure:"post_logout_redirect_url"`
	Scopes                        []string      `mapstructure:"scopes"`
	ClockSkew                     time.Duration `mapstructure:"clock_skew"`
	JWKSCacheTTL                  time.Duration `mapstructure:"jwks_cache_ttl"`
	RequireIntrospectionForWrites bool          `mapstructure:"require_introspection_for_writes"`
}

type SigningConfig struct {
	HubKeyPath                string   `mapstructure:"hub_key_path"`
	RequirePublisherSignature bool     `mapstructure:"require_publisher_signature"`
	AllowedAlgorithms         []string `mapstructure:"allowed_algorithms"`
	RevocationURL             string   `mapstructure:"revocation_url"`
	HostedCustody             struct {
		Enabled   bool   `mapstructure:"enabled"`
		Backend   string `mapstructure:"backend"`
		KeyPrefix string `mapstructure:"key_prefix"`
	} `mapstructure:"hosted_custody"`
}

type FederationConfig struct {
	Enabled             bool          `mapstructure:"enabled"`
	PublicRead          bool          `mapstructure:"public_read"`
	SyncInterval        time.Duration `mapstructure:"sync_interval"`
	MaxPagesPerRun      int           `mapstructure:"max_pages_per_run"`
	MaxConcurrentBlobs  int           `mapstructure:"max_concurrent_blobs"`
	MaxMirrorBytes      int64         `mapstructure:"max_mirror_bytes"`
	AllowInsecureHTTP   bool          `mapstructure:"allow_insecure_http"`
	AllowedPrivateCIDRs []string      `mapstructure:"allowed_private_cidrs"`
}

type WebConfig struct {
	Enable             bool          `mapstructure:"enable"`
	Theme              string        `mapstructure:"theme"`
	FeaturedNamespaces []string      `mapstructure:"featured_namespaces"`
	LandingCards       int           `mapstructure:"landing_cards"`
	LandingTTL         time.Duration `mapstructure:"landing_ttl"`
	AllowWebAuthoring  bool          `mapstructure:"allow_web_authoring"`
	Analytics          bool          `mapstructure:"analytics"`
}

type LimitsConfig struct {
	AnonymousSearchPerMin   int `mapstructure:"anonymous_search_per_min"`
	AuthenticatedReadPerMin int `mapstructure:"authenticated_read_per_min"`
	PublishPerHour          int `mapstructure:"publish_per_hour"`
	FederationSyncPerMin    int `mapstructure:"federation_sync_per_min"`
	MaxNamespacesPerUser    int `mapstructure:"max_namespaces_per_user"`
	MaxVersionsPerAsset     int `mapstructure:"max_versions_per_asset"`
}

type ObservabilityConfig struct {
	LogLevel        string  `mapstructure:"log_level"`
	LogFormat       string  `mapstructure:"log_format"`
	OTelEndpoint    string  `mapstructure:"otel_endpoint"`
	OTelSampleRatio float64 `mapstructure:"otel_sample_ratio"`
	MetricsEnabled  bool    `mapstructure:"metrics_enabled"`
	PprofEnabled    bool    `mapstructure:"pprof_enabled"`
}

type FeaturesConfig struct {
	Federation  bool `mapstructure:"federation"`
	Meilisearch bool `mapstructure:"meilisearch"`
	Webhooks    bool `mapstructure:"webhooks"`
	Sigstore    bool `mapstructure:"sigstore"`
}

// VaultConfig is defined in internal/secrets.

// NewViper builds a viper instance with defaults, env binding, and (if path is
// set) the config file loaded.
func NewViper(path string) (*viper.Viper, error) {
	v := viper.New()
	v.SetEnvPrefix("AGENTHUB")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	applyDefaults(v)
	// Viper only consults AutomaticEnv during Unmarshal for keys it knows
	// about. Keys without a default (database.dsn, auth.client_id, ...)
	// would otherwise ignore AGENTHUB_* overrides, so bind every known key.
	bindEnvKeys(v)

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config file %q: %w", path, err)
		}
	}
	return v, nil
}

// bindEnvKeys registers every mapstructure key with Viper. AutomaticEnv alone
// is not enough for Unmarshal: Viper only looks up environment variables for
// keys it already knows, and keys without a default would be invisible.
func bindEnvKeys(v *viper.Viper) {
	bindStructEnv(v, "", reflect.TypeOf(Config{}))
}

func bindStructEnv(v *viper.Viper, prefix string, t reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("mapstructure"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		key := tag
		if prefix != "" {
			key = prefix + "." + tag
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		// time.Duration is an int64; everything else that is a struct
		// (including the anonymous hosted_custody block) is a section.
		if ft.Kind() == reflect.Struct && ft != reflect.TypeOf(time.Duration(0)) {
			bindStructEnv(v, key, ft)
			continue
		}
		_ = v.BindEnv(key)
	}
}

func applyDefaults(v *viper.Viper) {
	// server
	v.SetDefault("env", "dev")
	v.SetDefault("server.http_addr", ":8080")
	v.SetDefault("server.grpc_addr", ":9090")
	v.SetDefault("server.metrics_addr", ":9100")
	v.SetDefault("server.public_url", "http://localhost:8080")
	v.SetDefault("server.read_timeout", "10s")
	v.SetDefault("server.write_timeout", "30s")
	v.SetDefault("server.idle_timeout", "120s")
	v.SetDefault("server.shutdown_grace", "20s")
	v.SetDefault("server.max_request_bytes", 4194304)
	v.SetDefault("server.rate_limit_per_sec", 100)
	v.SetDefault("server.trusted_proxies", []string{"10.0.0.0/8"})
	// database
	v.SetDefault("database.max_open_conns", 20)
	v.SetDefault("database.max_idle_conns", 10)
	v.SetDefault("database.conn_max_lifetime", "30m")
	v.SetDefault("database.statement_timeout", "15s")
	v.SetDefault("database.migrate_on_start", false)
	// storage
	v.SetDefault("storage.kind", "s3")
	v.SetDefault("storage.region", "hu-1")
	v.SetDefault("storage.bucket_bundles", "agenthub-bundles")
	v.SetDefault("storage.bucket_blobs", "agenthub-blobs")
	v.SetDefault("storage.bucket_exports", "agenthub-exports")
	v.SetDefault("storage.use_path_style", false)
	v.SetDefault("storage.presign_ttl", "5m")
	v.SetDefault("storage.server_side_encryption", "AES256")
	v.SetDefault("storage.max_bundle_bytes", 67108864)
	v.SetDefault("storage.max_file_bytes", 10485760)
	// cache
	v.SetDefault("cache.kind", "valkey")
	v.SetDefault("cache.db", 0)
	v.SetDefault("cache.ttl_manifest", "10m")
	v.SetDefault("cache.ttl_resolve", "2m")
	v.SetDefault("cache.ttl_session", "12h")
	// auth
	v.SetDefault("auth.audience", "agenthub")
	v.SetDefault("auth.audience_allowlist", []string{"agenthub"})
	v.SetDefault("auth.scopes", []string{"openid", "profile", "email", "agenthub.read", "agenthub.publish", "agenthub.admin"})
	v.SetDefault("auth.clock_skew", "60s")
	v.SetDefault("auth.jwks_cache_ttl", "10m")
	v.SetDefault("auth.require_introspection_for_writes", true)
	// signing
	v.SetDefault("signing.require_publisher_signature", true)
	v.SetDefault("signing.allowed_algorithms", []string{"ed25519"})
	v.SetDefault("signing.hosted_custody.backend", "openbao-transit")
	v.SetDefault("signing.hosted_custody.key_prefix", "agenthub")
	// federation
	v.SetDefault("federation.enabled", false)
	v.SetDefault("federation.public_read", false)
	v.SetDefault("federation.sync_interval", "15m")
	v.SetDefault("federation.max_pages_per_run", 1000)
	v.SetDefault("federation.max_concurrent_blobs", 4)
	v.SetDefault("federation.max_mirror_bytes", 5368709120)
	v.SetDefault("federation.allow_insecure_http", false)
	// web
	v.SetDefault("web.enable", true)
	v.SetDefault("web.theme", "default")
	v.SetDefault("web.featured_namespaces", []string{"pilab", "pirat"})
	v.SetDefault("web.landing_cards", 12)
	v.SetDefault("web.landing_ttl", "60s")
	v.SetDefault("web.allow_web_authoring", true)
	v.SetDefault("web.analytics", false)
	// limits
	v.SetDefault("limits.anonymous_search_per_min", 60)
	v.SetDefault("limits.authenticated_read_per_min", 600)
	v.SetDefault("limits.publish_per_hour", 30)
	v.SetDefault("limits.federation_sync_per_min", 10)
	v.SetDefault("limits.max_namespaces_per_user", 20)
	v.SetDefault("limits.max_versions_per_asset", 500)
	// observability
	v.SetDefault("observability.log_level", "info")
	v.SetDefault("observability.log_format", "json")
	v.SetDefault("observability.otel_sample_ratio", 0.1)
	v.SetDefault("observability.metrics_enabled", true)
	v.SetDefault("observability.pprof_enabled", false)
	// features
	v.SetDefault("features.federation", false)
	v.SetDefault("features.meilisearch", false)
	v.SetDefault("features.webhooks", false)
	v.SetDefault("features.sigstore", false)
	// vault
	v.SetDefault("vault.path", "secret/data/AgentHub")
	v.SetDefault("vault.auth_path", "kubernetes")
	v.SetDefault("vault.timeout", "15s")
}

// Decode unmarshals the viper instance into a Config, applies secrets and
// validates. It is safe to call multiple times.
func Decode(ctx context.Context, v *viper.Viper, flags *pflag.FlagSet, bindings map[string]string) (*Config, error) {
	// bind flags (highest precedence)
	for key, flagName := range bindings {
		if flags == nil {
			continue
		}
		if f := flags.Lookup(flagName); f != nil {
			if err := v.BindPFlag(key, f); err != nil {
				return nil, fmt.Errorf("bind flag %q: %w", flagName, err)
			}
		}
	}

	// vault secrets: load before final unmarshal so ${VAR} expansion works.
	vaultCfg := secrets.VaultConfig{
		Address:             v.GetString("vault.address"),
		Path:                v.GetString("vault.path"),
		Token:               v.GetString("vault.token"),
		AuthPath:            v.GetString("vault.auth_path"),
		Role:                v.GetString("vault.role"),
		ServiceAccountToken: v.GetString("vault.service_account_token"),
		Timeout:             v.GetDuration("vault.timeout"),
	}
	if vaultCfg.Address != "" {
		if err := secrets.ApplyVaultEnv(ctx, vaultCfg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: vault secrets unavailable: %v\n", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	expandVars(&cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Load is a convenience wrapper over NewViper + Decode.
func Load(ctx context.Context, path string, flags *pflag.FlagSet, bindings map[string]string) (*Config, error) {
	v, err := NewViper(path)
	if err != nil {
		return nil, err
	}
	return Decode(ctx, v, flags, bindings)
}

// expandVars walks the config struct and expands ${VAR}/$VAR in strings.
func expandVars(cfg *Config) {
	rv := reflect.ValueOf(cfg).Elem()
	walkExpand(rv)
}

func walkExpand(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(os.ExpandEnv(v.String()))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			walkExpand(v.Field(i))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			walkExpand(v.Elem())
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			walkExpand(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			walkExpand(v.MapIndex(k))
		}
	}
}

// minSessionSecretLen is the shortest accepted auth.session_secret: the
// cookie HMAC key is the only thing standing between a stolen cookie and a
// forged session.
const minSessionSecretLen = 32

// Validate implements the fail-fast rules from operations.md §2.2.
func (c *Config) Validate() error {
	var errs []string
	add := func(msg string) { errs = append(errs, msg) }

	if c.Env == "" {
		c.Env = "dev"
	}
	switch c.Env {
	case "dev", "staging", "prod", "airgap":
	default:
		add(fmt.Sprintf("env: unknown value %q", c.Env))
	}
	if c.Server.HTTPAddr == "" {
		add("server.http_addr is required")
	}
	if c.Server.PublicURL != "" {
		if u := parseURL(c.Server.PublicURL); u == "" {
			add("server.public_url is not a valid URL")
		}
	}
	if c.Database.DSN != "" {
		if !strings.HasPrefix(c.Database.DSN, "postgres://") && !strings.HasPrefix(c.Database.DSN, "postgresql://") {
			add("database.dsn must be a postgres:// URL")
		}
	} else {
		add("database.dsn is required")
	}
	for _, k := range []string{c.Storage.Kind} {
		if k != "s3" && k != "filesystem" {
			add(fmt.Sprintf("storage.kind: unknown value %q", k))
		}
	}
	for _, k := range []string{c.Cache.Kind} {
		if k != "valkey" && k != "memory" {
			add(fmt.Sprintf("cache.kind: unknown value %q", k))
		}
	}
	if c.Auth.RedirectURL != "" && c.Server.PublicURL != "" {
		if originOf(c.Auth.RedirectURL) != originOf(c.Server.PublicURL) {
			add("auth.redirect_url origin must match server.public_url")
		}
	}
	if c.Auth.ClientID != "" {
		// OIDC BFF (M3.5): /auth/login, /auth/callback and /auth/logout
		// discover their endpoints from the issuer, sign their cookies
		// with auth.session_secret and need a redirect URI to come back
		// to. All three are derived from these keys alone.
		if c.Auth.Issuer == "" {
			add("auth.issuer is required when auth.client_id is set")
		}
		if c.Auth.SessionSecret == "" {
			add("auth.session_secret is required when auth.client_id is set")
		}
		if c.Auth.RedirectURL == "" && c.Server.PublicURL == "" {
			add("auth.redirect_url (or server.public_url) is required when auth.client_id is set")
		}
	}
	if c.Auth.SessionSecret != "" && len(c.Auth.SessionSecret) < minSessionSecretLen {
		add(fmt.Sprintf("auth.session_secret must be at least %d characters", minSessionSecretLen))
	}
	if c.Auth.Issuer != "" {
		// Shadow SSO mode (M3.3): server.New builds an sso.Verifier from
		// these fields and refuses to start on the same problems.
		if s := parseURL(c.Auth.Issuer); s != "http" && s != "https" {
			add("auth.issuer must be an absolute http(s) URL")
		}
		if c.Auth.Audience == "" && len(c.Auth.AudienceAllowlist) == 0 {
			add("auth.audience (or auth.audience_allowlist) is required when auth.issuer is set")
		}
		if c.Auth.JWKSURL != "" {
			if s := parseURL(c.Auth.JWKSURL); s != "http" && s != "https" {
				add("auth.jwks_url must be an absolute http(s) URL")
			}
		}
		if c.Auth.DiscoveryURL != "" {
			if s := parseURL(c.Auth.DiscoveryURL); s != "http" && s != "https" {
				add("auth.discovery_url must be an absolute http(s) URL")
			}
		}
		if c.Auth.ClockSkew < 0 {
			add("auth.clock_skew must not be negative")
		}
	}
	if c.Signing.HostedCustody.Enabled {
		if c.Signing.HostedCustody.Backend == "" {
			add("signing.hosted_custody.backend is required when enabled")
		}
	}
	if c.Federation.Enabled && c.Signing.HubKeyPath == "" {
		add("federation.enabled requires signing.hub_key_path")
	}
	if c.Vault.Address != "" {
		if c.Vault.Path == "" {
			add("vault.path is required when vault.address is set")
		}
	}

	// production-only rules
	if c.Env == "prod" {
		if !c.Signing.RequirePublisherSignature {
			add("signing.require_publisher_signature must be true in prod")
		}
		if c.Storage.Kind == "filesystem" {
			add("storage.kind must not be filesystem in prod")
		}
		if c.Cache.Kind == "memory" {
			add("cache.kind must not be memory in prod")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// RedactedYAML returns a YAML document with secret-looking fields masked.
func (c *Config) RedactedYAML() ([]byte, error) {
	// marshal via viper-like map: use mapstructure by re-running through yaml
	// We use reflect-based redaction on a copy of c.
	cp := *c
	redactValue(reflect.ValueOf(&cp).Elem())
	return yaml.Marshal(&cp)
}

func redactValue(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			name := strings.ToLower(v.Type().Field(i).Name)
			if f.Kind() == reflect.String && isSecretField(name, v.Type().Field(i).Tag.Get("mapstructure")) {
				if f.CanSet() {
					f.SetString("***REDACTED***")
				}
			} else {
				redactValue(f)
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			redactValue(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			redactValue(v.MapIndex(k))
		}
	}
}

func isSecretField(fieldName, tag string) bool {
	key := tag
	if key == "" {
		key = fieldName
	}
	lk := strings.ToLower(key)
	for _, s := range []string{"password", "secret", "token", "dsn", "client_secret", "hub_key_path"} {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

func parseURL(s string) string {
	if !strings.Contains(s, "://") {
		return ""
	}
	parts := strings.SplitN(s, "://", 2)
	if len(parts) != 2 || parts[1] == "" {
		return ""
	}
	return parts[0]
}

func originOf(s string) string {
	// scheme://host[:port]
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}
