package standalone

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"gopkg.in/yaml.v3"
)

// Config is the YAML-loadable configuration for the standalone server.
// Defaults mirror SECURITY.md §14 and dbadmin.DefaultConfig().
type Config struct {
	Listen        string              `yaml:"listen"`
	TLS           TLSConfig           `yaml:"tls"`
	Storage       StorageConfig       `yaml:"storage"`
	KEK           KEKConfig           `yaml:"kek"`
	Session       SessionConfig       `yaml:"session"`
	Auth          AuthConfig          `yaml:"auth"`
	RateLimits    RateLimitConfig     `yaml:"rate_limits"`
	Query         QueryConfig         `yaml:"query"`
	Audit         AuditConfig         `yaml:"audit"`
	Network       NetworkConfig       `yaml:"network"`
	Logging       LoggingConfig       `yaml:"logging"`
	CSP           CSPConfig           `yaml:"csp"`
	ForbiddenList ForbiddenListConfig `yaml:"forbidden_list"`
}

// TLSConfig configures the listener TLS settings.
type TLSConfig struct {
	CertFile    string     `yaml:"cert_file"`
	KeyFile     string     `yaml:"key_file"`
	MinVersion  string     `yaml:"min_version"`
	CipherSuite string     `yaml:"cipher_suite"`
	MTLS        MTLSConfig `yaml:"mtls"`
}

// MTLSConfig configures client certificate enforcement.
type MTLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CABundle string `yaml:"ca_bundle"`
}

// StorageConfig points to the per-process state files.
type StorageConfig struct {
	DBPath        string `yaml:"db_path"`
	AuditLogPath  string `yaml:"audit_log_path"`
	HistoryDBPath string `yaml:"history_db_path"`
}

// KEKConfig points to the key encryption key file.
type KEKConfig struct {
	File          string `yaml:"file"`
	RotateOnBoot  bool   `yaml:"rotate_on_boot"`
}

// SessionConfig extends dbadmin.SessionConfig with binding toggles.
type SessionConfig struct {
	IdleTTL          time.Duration `yaml:"idle_ttl"`
	AbsoluteTTL      time.Duration `yaml:"absolute_ttl"`
	MaxConcurrent    int           `yaml:"max_concurrent"`
	BindToIPClass    bool          `yaml:"bind_to_ip_class"`
	BindToUAHash     bool          `yaml:"bind_to_ua_hash"`
}

// AuthConfig captures password + MFA policy.
type AuthConfig struct {
	PasswordMinLength int          `yaml:"password_min_length"`
	Argon2            Argon2Config `yaml:"argon2"`
	HIBPCheck         bool         `yaml:"hibp_check"`
	MFA               MFAConfig    `yaml:"mfa"`
}

// Argon2Config captures the Argon2id parameters.
type Argon2Config struct {
	Time      uint32 `yaml:"time"`
	MemoryKiB uint32 `yaml:"memory_kib"`
	Threads   uint8  `yaml:"threads"`
	KeyLen    uint32 `yaml:"key_len"`
	SaltLen   uint32 `yaml:"salt_len"`
}

// MFAConfig captures multi-factor policy.
type MFAConfig struct {
	RequiredFor      []string `yaml:"required_for"`
	TOTPEnabled      bool     `yaml:"totp_enabled"`
	WebAuthnEnabled  bool     `yaml:"webauthn_enabled"`
	RecoveryCodes    int      `yaml:"recovery_codes"`
}

// RateLimitConfig extends dbadmin.RateLimitConfig with the lockout
// escalation ladder.
type RateLimitConfig struct {
	LoginPerIP15m              int     `yaml:"login_per_ip_15m"`
	LoginPerUser15m            int     `yaml:"login_per_user_15m"`
	QueryPerUserPerMin         int     `yaml:"query_per_user_per_min"`
	QueryPerIPPerMin           int     `yaml:"query_per_ip_per_min"`
	LockoutEscalationMinutes   []int   `yaml:"lockout_escalation_minutes"`
}

// QueryConfig mirrors dbadmin.QueryConfig.
type QueryConfig struct {
	TimeoutDefault           time.Duration `yaml:"timeout_default"`
	TimeoutMax               time.Duration `yaml:"timeout_max"`
	ResultRowsDefault        int           `yaml:"result_rows_default"`
	ResultRowsMax            int           `yaml:"result_rows_max"`
	ResultBytesDefault       int64         `yaml:"result_bytes_default"`
	ResultBytesMax           int64         `yaml:"result_bytes_max"`
	ConcurrentPerUserPerConn int           `yaml:"concurrent_per_user_per_conn"`
	ConcurrentMax            int           `yaml:"concurrent_max"`
	SQLInputMaxBytes         int           `yaml:"sql_input_max_bytes"`
	PoolSizePerConn          int           `yaml:"pool_size_per_conn"`
	PoolIdleTimeout          time.Duration `yaml:"pool_idle_timeout"`
}

// AuditConfig contains the audit knobs.
type AuditConfig struct {
	SampleReadQueries     float64                `yaml:"sample_read_queries"`
	RedactSensitiveParams bool                   `yaml:"redact_sensitive_params"`
	ChainSigning          ChainSigningConfig     `yaml:"chain_signing"`
	Forwarders            []AuditForwarderConfig `yaml:"forwarders"`
	RetentionDays         int                    `yaml:"retention_days"`
}

// ChainSigningConfig configures HMAC-signed chain heads.
type ChainSigningConfig struct {
	Enabled       bool          `yaml:"enabled"`
	KeyFile       string        `yaml:"key_file"`
	EveryEvents   int           `yaml:"every_events"`
	Every         time.Duration `yaml:"every"`
}

// AuditForwarderConfig describes one forwarder.
//
// OPS-14: the comment used to advertise "s3" as a supported kind, but
// buildForwarder rejects anything other than syslog/webhook. The set
// is exhaustively listed below.
type AuditForwarderConfig struct {
	Kind     string `yaml:"kind"` // "syslog" | "webhook"
	Address  string `yaml:"address"`
	Protocol string `yaml:"protocol"`
	Facility string `yaml:"facility"`
	URL      string `yaml:"url"`
	SecretFile string `yaml:"secret_file"`
}

// NetworkConfig mirrors dbadmin.NetworkConfig.
type NetworkConfig struct {
	CSPReportURI string `yaml:"csp_report_uri"`
	// TrustedProxies is a list of CIDRs (e.g. "127.0.0.1/32",
	// "10.0.0.0/8") whose forwarded headers (X-Forwarded-For) we honor
	// for client-IP derivation. Empty list (default) means
	// X-Forwarded-For is ignored entirely — every request's IP class is
	// derived from r.RemoteAddr (SEC-07).
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// LoggingConfig captures slog plumbing.
type LoggingConfig struct {
	Level       string `yaml:"level"`
	Format      string `yaml:"format"`
	Destination string `yaml:"destination"`
}

// CSPConfig captures content-security-policy values.
type CSPConfig struct {
	ReportURI            string `yaml:"report_uri"`
	RequireTrustedTypes  bool   `yaml:"require_trusted_types"`
}

// ForbiddenListConfig captures operator additions to the classifier
// denylist.
type ForbiddenListConfig struct {
	AdditionalFunctionNames []string `yaml:"additional_function_names"`
}

// DefaultConfig returns the canonical secure-default Config.
func DefaultConfig() Config {
	return Config{
		Listen: "127.0.0.1:7878",
		TLS: TLSConfig{
			MinVersion:  "TLS1.3",
			CipherSuite: "mozilla_modern",
		},
		Storage: StorageConfig{
			DBPath:        "/var/lib/aura-db/aura.db",
			AuditLogPath:  "/var/lib/aura-db/audit.log",
			HistoryDBPath: "/var/lib/aura-db/history.db",
		},
		KEK: KEKConfig{File: DefaultKEKPath},
		Session: SessionConfig{
			IdleTTL:       15 * time.Minute,
			AbsoluteTTL:   8 * time.Hour,
			MaxConcurrent: 5,
			BindToIPClass: true,
			BindToUAHash:  true,
		},
		Auth: AuthConfig{
			PasswordMinLength: 14,
			Argon2: Argon2Config{
				Time:      3,
				MemoryKiB: 64 * 1024,
				Threads:   4,
				KeyLen:    32,
				SaltLen:   16,
			},
			HIBPCheck: true,
			MFA: MFAConfig{
				RequiredFor:     []string{"writer", "dba", "owner"},
				TOTPEnabled:     true,
				WebAuthnEnabled: false,
				RecoveryCodes:   RecoveryCodeCount,
			},
		},
		RateLimits: RateLimitConfig{
			LoginPerIP15m:            10,
			LoginPerUser15m:          5,
			QueryPerUserPerMin:       30,
			QueryPerIPPerMin:         60,
			LockoutEscalationMinutes: []int{15, 30, 60, 120, 240, 480, 1440},
		},
		Query: QueryConfig{
			TimeoutDefault:           30 * time.Second,
			TimeoutMax:               5 * time.Minute,
			ResultRowsDefault:        10_000,
			ResultRowsMax:            100_000,
			ResultBytesDefault:       50 * 1024 * 1024,
			ResultBytesMax:           500 * 1024 * 1024,
			ConcurrentPerUserPerConn: 1,
			ConcurrentMax:            3,
			SQLInputMaxBytes:         1024 * 1024,
			PoolSizePerConn:          4,
			PoolIdleTimeout:          5 * time.Minute,
		},
		Audit: AuditConfig{
			SampleReadQueries:     0.01,
			RedactSensitiveParams: true,
			ChainSigning: ChainSigningConfig{
				EveryEvents: 1000,
				Every:       5 * time.Minute,
			},
			RetentionDays: 365,
		},
		Network: NetworkConfig{},
		Logging: LoggingConfig{
			Level: "info",
			// OPS-05: json by default — structured logs are required
			// for SIEM ingestion and grep-with-jq workflows. Text
			// remains supported via explicit configuration.
			Format:      "json",
			Destination: "stderr",
		},
		CSP: CSPConfig{
			RequireTrustedTypes: true,
		},
	}
}

// LoadConfig reads YAML from path, fills in defaults for unset fields,
// and validates.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("standalone: read config %q: %w", path, err)
	}

	// Reject inlined secrets — the YAML must reference files, never
	// embed key material.
	if err := rejectInlineSecrets(raw); err != nil {
		return Config{}, err
	}

	// Start with defaults and overlay the user's file. yaml.v3 fills
	// only the keys present in the document, leaving the rest at their
	// default values.
	cfg := DefaultConfig()
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("standalone: parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate enforces internal consistency. Mirrors dbadmin.Config.validate
// for the engine-shared subset and adds standalone-specific checks.
//
// cfg-validate-skips-merge-then-validate: this duplicates the
// engine-shared subset of dbadmin.Config.validate so we surface
// failures at YAML load time without having to round-trip through
// engine New(). Keep the two in lockstep — any new invariant added to
// dbadmin.Config.validate MUST be mirrored here and added to the
// drift test in config_test.go.
func (c *Config) Validate() error {
	if c.Session.IdleTTL > c.Session.AbsoluteTTL {
		return fmt.Errorf("standalone: session.idle_ttl (%s) must not exceed absolute_ttl (%s)",
			c.Session.IdleTTL, c.Session.AbsoluteTTL)
	}
	if c.Session.MaxConcurrent < 1 {
		return fmt.Errorf("standalone: session.max_concurrent must be >= 1")
	}
	if c.Query.TimeoutDefault > c.Query.TimeoutMax {
		return fmt.Errorf("standalone: query.timeout_default exceeds timeout_max")
	}
	if c.Query.ResultRowsDefault > c.Query.ResultRowsMax {
		return fmt.Errorf("standalone: query.result_rows_default exceeds result_rows_max")
	}
	if c.Query.ResultBytesDefault > c.Query.ResultBytesMax {
		return fmt.Errorf("standalone: query.result_bytes_default exceeds result_bytes_max")
	}
	if c.Query.ConcurrentPerUserPerConn > c.Query.ConcurrentMax {
		return fmt.Errorf("standalone: query.concurrent_per_user_per_conn exceeds concurrent_max")
	}
	if c.Audit.SampleReadQueries < 0 || c.Audit.SampleReadQueries > 1 {
		return fmt.Errorf("standalone: audit.sample_read_queries must be in [0,1]")
	}
	if c.Auth.PasswordMinLength < 14 {
		return fmt.Errorf("standalone: auth.password_min_length must be >= 14")
	}
	if c.Auth.Argon2.Time < 2 {
		return fmt.Errorf("standalone: auth.argon2.time must be >= 2")
	}
	if c.Auth.Argon2.MemoryKiB < 32*1024 {
		return fmt.Errorf("standalone: auth.argon2.memory_kib must be >= 32768")
	}
	if len(c.RateLimits.LockoutEscalationMinutes) == 0 {
		return fmt.Errorf("standalone: rate_limits.lockout_escalation_minutes must be non-empty")
	}
	for i, m := range c.RateLimits.LockoutEscalationMinutes {
		if m <= 0 {
			return fmt.Errorf("standalone: lockout escalation must be > 0; got %d at index %d", m, i)
		}
		if i > 0 && m <= c.RateLimits.LockoutEscalationMinutes[i-1] {
			return fmt.Errorf("standalone: lockout escalation must be strictly increasing")
		}
	}
	if (c.TLS.CertFile == "") != (c.TLS.KeyFile == "") {
		return fmt.Errorf("standalone: tls.cert_file and tls.key_file must both be set or both empty")
	}
	if c.TLS.MTLS.Enabled && (c.TLS.CertFile == "" || c.TLS.MTLS.CABundle == "") {
		return fmt.Errorf("standalone: tls.mtls.enabled requires tls.cert_file and tls.mtls.ca_bundle")
	}
	for _, p := range []string{c.Storage.DBPath, c.Storage.AuditLogPath, c.Storage.HistoryDBPath} {
		if p == "" {
			return fmt.Errorf("standalone: storage paths must be non-empty")
		}
		// :memory: is permitted for tests.
		if p != ":memory:" && !filepath.IsAbs(p) {
			return fmt.Errorf("standalone: storage path %q must be absolute", p)
		}
	}
	if c.KEK.File != "" && !filepath.IsAbs(c.KEK.File) {
		return fmt.Errorf("standalone: kek.file %q must be absolute", c.KEK.File)
	}
	if c.KEK.RotateOnBoot && os.Getenv("AURA_DB_ALLOW_ROTATE_ON_BOOT") != "1" {
		return fmt.Errorf("standalone: kek.rotate_on_boot requires env AURA_DB_ALLOW_ROTATE_ON_BOOT=1")
	}
	for i, cidr := range c.Network.TrustedProxies {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("standalone: network.trusted_proxies[%d] %q is not a valid CIDR: %w", i, cidr, err)
		}
	}
	// OPS-06: validate the few enum-shaped strings so a typo at the
	// config file lands a loud error at boot instead of silently
	// defaulting to "the friendliest match".
	switch strings.TrimSpace(c.TLS.MinVersion) {
	case "", "TLS1.2", "TLS1.3":
	default:
		return fmt.Errorf("standalone: tls.min_version %q invalid (want TLS1.2 or TLS1.3)", c.TLS.MinVersion)
	}
	switch strings.ToLower(strings.TrimSpace(c.Logging.Level)) {
	case "", "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("standalone: logging.level %q invalid (want debug|info|warn|error)", c.Logging.Level)
	}
	switch strings.ToLower(strings.TrimSpace(c.Logging.Format)) {
	case "", "text", "json":
	default:
		return fmt.Errorf("standalone: logging.format %q invalid (want text|json)", c.Logging.Format)
	}
	if dest := strings.TrimSpace(c.Logging.Destination); dest != "" && dest != "stderr" && dest != "stdout" && !strings.HasPrefix(dest, "file:") {
		return fmt.Errorf("standalone: logging.destination %q invalid (want stderr|stdout|file:<path>)", c.Logging.Destination)
	}
	// OPS-16: validate kek.file mode at config load time when the
	// file exists. This catches operator footguns (KEK pre-created at
	// 0644) at `aura-db config validate` or `serve --dry-run` instead
	// of waiting until the first request that decrypts a credential.
	if c.KEK.File != "" {
		if st, err := os.Stat(c.KEK.File); err == nil {
			mode := st.Mode().Perm()
			if mode == 0 {
				return fmt.Errorf("standalone: kek.file %q has mode 0 (set to 0400)", c.KEK.File)
			}
			if mode&^0o400 != 0 {
				return fmt.Errorf("standalone: kek.file %q has mode %o; want 0400 or stricter", c.KEK.File, mode)
			}
		}
	}
	return nil
}

// TrustedProxyNets returns the parsed trusted-proxy CIDRs. Returns nil
// when none are configured.
func (c *Config) TrustedProxyNets() ([]*net.IPNet, error) {
	if len(c.Network.TrustedProxies) == 0 {
		return nil, nil
	}
	out := make([]*net.IPNet, 0, len(c.Network.TrustedProxies))
	for _, cidr := range c.Network.TrustedProxies {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("standalone: trusted_proxies %q: %w", cidr, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// ToDBAdminConfig assembles the engine-facing subset of Config.
func (c *Config) ToDBAdminConfig() dbadmin.Config {
	return dbadmin.Config{
		Session: dbadmin.SessionConfig{
			IdleTTL:       c.Session.IdleTTL,
			AbsoluteTTL:   c.Session.AbsoluteTTL,
			MaxConcurrent: c.Session.MaxConcurrent,
		},
		RateLimits: dbadmin.RateLimitConfig{
			LoginPerIP15m:      c.RateLimits.LoginPerIP15m,
			LoginPerUser15m:    c.RateLimits.LoginPerUser15m,
			QueryPerUserPerMin: c.RateLimits.QueryPerUserPerMin,
			QueryPerIPPerMin:   c.RateLimits.QueryPerIPPerMin,
		},
		Query: dbadmin.QueryConfig{
			TimeoutDefault:           c.Query.TimeoutDefault,
			TimeoutMax:               c.Query.TimeoutMax,
			ResultRowsDefault:        c.Query.ResultRowsDefault,
			ResultRowsMax:            c.Query.ResultRowsMax,
			ResultBytesDefault:       c.Query.ResultBytesDefault,
			ResultBytesMax:           c.Query.ResultBytesMax,
			ConcurrentPerUserPerConn: c.Query.ConcurrentPerUserPerConn,
			ConcurrentMax:            c.Query.ConcurrentMax,
			SQLInputMaxBytes:         c.Query.SQLInputMaxBytes,
			PoolSizePerConn:          c.Query.PoolSizePerConn,
			PoolIdleTimeout:          c.Query.PoolIdleTimeout,
		},
		Audit: dbadmin.AuditConfig{
			SampleReadQueries:     c.Audit.SampleReadQueries,
			RedactSensitiveParams: c.Audit.RedactSensitiveParams,
		},
		Network: dbadmin.NetworkConfig{
			TLSMin:       c.TLS.MinVersion,
			MTLSEnabled:  c.TLS.MTLS.Enabled,
			CSPReportURI: c.Network.CSPReportURI,
		},
	}
}

// PasswordPolicy returns the Argon2id policy implied by the config.
func (c *Config) PasswordPolicy() PasswordPolicy {
	return PasswordPolicy{
		Time:    c.Auth.Argon2.Time,
		Memory:  c.Auth.Argon2.MemoryKiB,
		Threads: c.Auth.Argon2.Threads,
		KeyLen:  c.Auth.Argon2.KeyLen,
		SaltLen: c.Auth.Argon2.SaltLen,
		MinLen:  c.Auth.PasswordMinLength,
		Version: 1,
	}
}

// LockoutEscalation returns the escalation ladder as durations.
func (c *Config) LockoutEscalation() []time.Duration {
	out := make([]time.Duration, len(c.RateLimits.LockoutEscalationMinutes))
	for i, m := range c.RateLimits.LockoutEscalationMinutes {
		out[i] = time.Duration(m) * time.Minute
	}
	return out
}

// AuthRuntime composes the values Auth needs at request time.
func (c *Config) AuthRuntime() AuthRuntimeConfig {
	return AuthRuntimeConfig{
		IdleTTL:         c.Session.IdleTTL,
		AbsoluteTTL:     c.Session.AbsoluteTTL,
		MaxConcurrent:   c.Session.MaxConcurrent,
		BindIPClass:     c.Session.BindToIPClass,
		BindUAHash:      c.Session.BindToUAHash,
		Password:        c.PasswordPolicy(),
		LoginPerIP15m:   c.RateLimits.LoginPerIP15m,
		LoginPerUser15m: c.RateLimits.LoginPerUser15m,
		Escalation:      c.LockoutEscalation(),
		StepUpTTL:       DefaultStepUpTTL(),
	}
}

// rejectInlineSecrets refuses YAML that defines any key suggesting a
// raw secret value. Operators must use *_file paths instead.
func rejectInlineSecrets(raw []byte) error {
	forbidden := []string{
		"\nkek_inline:", "\n  kek_inline:",
		"\nsecrets:", "\n  secrets:",
		"\npassword:", "\n  password:",
	}
	body := "\n" + string(raw)
	for _, f := range forbidden {
		if strings.Contains(body, f) {
			return fmt.Errorf("standalone: secrets must not be inlined in config; use a *_file path instead (offending key: %q)",
				strings.TrimSpace(strings.Trim(f, "\n: ")))
		}
	}
	return nil
}
