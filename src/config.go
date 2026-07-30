package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Configuration
//
// secondbrain has two configuration modes and they are deliberately unequal.
//
// The common case is a single person running a single container for their own
// notes. That case needs no file at all: SECONDBRAIN_USERNAME and
// SECONDBRAIN_PASSWORD are enough, and everything else has a working default.
//
// The uncommon case - several people, or one person who wants a vault kept
// away from a particular client - mounts a config.yaml. When that file is
// present it defines the users; the environment then only supplies the
// operational settings. Mixing the two would create the question "which
// password wins", and a question like that in an auth path is a bug waiting
// to be written.
// ---------------------------------------------------------------------------

const (
	defaultListen       = ":2020"
	defaultDataDir      = "/data"
	defaultVaultName    = "default"
	defaultTokenTTL     = 12 * time.Hour
	defaultCodeTTL      = 60 * time.Second
	defaultMaxResponse  = 256 << 10 // 256 KiB per tool result
	defaultLoginLimit   = "10/m"
	defaultTrashRetain  = 30 * 24 * time.Hour
	defaultMaxNoteBytes = 4 << 20 // refuse to index or read anything larger
)

// vaultNameRe is the whole reason path traversal is not a concern for vault
// names: a name that matches this cannot contain a separator, a dot or a
// null byte, so filepath.Join(dataDir, name) cannot escape dataDir.
var vaultNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type Config struct {
	Listen         string   `yaml:"listen"`
	PublicURL      string   `yaml:"public_url"`
	AllowedOrigins []string `yaml:"allowed_origins"`

	DataDir      string `yaml:"data_dir"`
	DefaultVault string `yaml:"default_vault"`

	MaxResponseBytes int           `yaml:"max_response_bytes"`
	TokenTTL         time.Duration `yaml:"-"`
	CodeTTL          time.Duration `yaml:"-"`
	TrashRetention   time.Duration `yaml:"-"`
	LoginRateLimit   Rate          `yaml:"-"`

	RawTokenTTL       string `yaml:"token_ttl"`
	RawCodeTTL        string `yaml:"code_ttl"`
	RawTrashRetention string `yaml:"trash_retention"`
	RawLoginRate      string `yaml:"login_rate_limit"`

	Git       bool   `yaml:"git"`
	GitRemote string `yaml:"git_remote"`
	GitAuthor string `yaml:"git_author"`
	GitEmail  string `yaml:"git_email"`
	GitToken  string `yaml:"git_token"`

	Users map[string]*User `yaml:"-"`

	RawUsers []*User `yaml:"users"`

	// Source records where the configuration came from, for the startup log.
	Source string `yaml:"-"`
}

type User struct {
	Name     string `yaml:"name"`
	Password string `yaml:"password"`

	// Vaults limits which vaults this user may address. Empty means every
	// vault in the data directory, including ones created later.
	Vaults []string `yaml:"vaults"`

	// ReadOnly turns off every mutating tool for this user. Useful for a
	// client you want to let read your notes but never touch them.
	ReadOnly bool `yaml:"read_only"`

	PasswordIsHash bool `yaml:"-"`
}

// CanUseVault reports whether the user may address the named vault.
func (u *User) CanUseVault(name string) bool {
	if len(u.Vaults) == 0 {
		return true
	}
	for _, v := range u.Vaults {
		if v == name {
			return true
		}
	}
	return false
}

// Issuer is the external base URL without a trailing slash.
func (c *Config) Issuer() string { return strings.TrimRight(c.PublicURL, "/") }

func (c *Config) endpoint(path string) string { return c.Issuer() + path }

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

// LoadConfig builds the configuration from the environment and, if a config
// file exists at path, from that file as well. path may be empty.
func LoadConfig(path string) (*Config, error) {
	c := &Config{
		Listen:           defaultListen,
		DataDir:          defaultDataDir,
		DefaultVault:     defaultVaultName,
		MaxResponseBytes: defaultMaxResponse,
		TokenTTL:         defaultTokenTTL,
		CodeTTL:          defaultCodeTTL,
		TrashRetention:   defaultTrashRetain,
		Git:              true,
		GitAuthor:        "secondbrain",
		GitEmail:         "secondbrain@localhost",
		Users:            map[string]*User{},
		Source:           "environment",
	}
	c.LoginRateLimit, _ = ParseRate(defaultLoginLimit)

	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(raw, c); err != nil {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
			c.Source = path
		case os.IsNotExist(err):
			// Not an error: the file is optional.
		default:
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
	}

	if err := c.applyRawDurations(); err != nil {
		return nil, err
	}
	if err := c.applyEnv(); err != nil {
		return nil, err
	}
	if err := c.buildUsers(); err != nil {
		return nil, err
	}
	return c, c.validate()
}

func (c *Config) applyRawDurations() error {
	pairs := []struct {
		raw string
		dst *time.Duration
		key string
	}{
		{c.RawTokenTTL, &c.TokenTTL, "token_ttl"},
		{c.RawCodeTTL, &c.CodeTTL, "code_ttl"},
		{c.RawTrashRetention, &c.TrashRetention, "trash_retention"},
	}
	for _, p := range pairs {
		if p.raw == "" {
			continue
		}
		d, err := time.ParseDuration(p.raw)
		if err != nil {
			return fmt.Errorf("%s: %w", p.key, err)
		}
		if d <= 0 {
			return fmt.Errorf("%s must be positive", p.key)
		}
		*p.dst = d
	}
	if c.RawLoginRate != "" {
		r, err := ParseRate(c.RawLoginRate)
		if err != nil {
			return fmt.Errorf("login_rate_limit: %w", err)
		}
		c.LoginRateLimit = r
	}
	return nil
}

func (c *Config) applyEnv() error {
	setString(&c.Listen, "SECONDBRAIN_LISTEN")
	setString(&c.PublicURL, "SECONDBRAIN_PUBLIC_URL")
	setString(&c.DataDir, "SECONDBRAIN_DATA")
	setString(&c.DefaultVault, "SECONDBRAIN_DEFAULT_VAULT")
	setString(&c.GitRemote, "SECONDBRAIN_GIT_REMOTE")
	setString(&c.GitAuthor, "SECONDBRAIN_GIT_AUTHOR")
	setString(&c.GitEmail, "SECONDBRAIN_GIT_EMAIL")

	if v := os.Getenv("SECONDBRAIN_GIT_TOKEN"); v != "" {
		c.GitToken = v
	}
	if c.GitToken != "" {
		// The push credential is a secret like any other, so it takes the
		// same env: and file: prefixes - otherwise config.yaml could not
		// reference a Docker secret and would have to hold the token itself.
		resolved, err := resolveSecret(c.GitToken)
		if err != nil {
			return fmt.Errorf("git_token: %w", err)
		}
		c.GitToken = resolved
	}
	if v := os.Getenv("SECONDBRAIN_ALLOWED_ORIGINS"); v != "" {
		c.AllowedOrigins = splitList(v)
	}
	if v := os.Getenv("SECONDBRAIN_MAX_RESPONSE_BYTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 4096 {
			return fmt.Errorf("SECONDBRAIN_MAX_RESPONSE_BYTES must be an integer >= 4096")
		}
		c.MaxResponseBytes = n
	}
	if v := os.Getenv("SECONDBRAIN_GIT"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("SECONDBRAIN_GIT must be true or false")
		}
		c.Git = b
	}
	for _, p := range []struct {
		env string
		dst *time.Duration
	}{
		{"SECONDBRAIN_TOKEN_TTL", &c.TokenTTL},
		{"SECONDBRAIN_CODE_TTL", &c.CodeTTL},
		{"SECONDBRAIN_TRASH_RETENTION", &c.TrashRetention},
	} {
		if v := os.Getenv(p.env); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return fmt.Errorf("%s must be a positive duration such as 12h", p.env)
			}
			*p.dst = d
		}
	}
	return nil
}

// buildUsers turns either the file's user list or the environment pair into
// the lookup map. The file wins outright when it defines any user.
func (c *Config) buildUsers() error {
	if len(c.RawUsers) > 0 {
		for _, u := range c.RawUsers {
			if u.Name == "" {
				return fmt.Errorf("every user needs a name")
			}
			if _, dup := c.Users[u.Name]; dup {
				return fmt.Errorf("user %q defined twice", u.Name)
			}
			pw, err := resolveSecret(u.Password)
			if err != nil {
				return fmt.Errorf("user %s: password: %w", u.Name, err)
			}
			if pw == "" {
				return fmt.Errorf("user %s: password is empty", u.Name)
			}
			u.Password = pw
			u.PasswordIsHash = strings.HasPrefix(pw, "bcrypt:")
			if u.PasswordIsHash {
				u.Password = strings.TrimPrefix(pw, "bcrypt:")
			}
			for _, v := range u.Vaults {
				if !vaultNameRe.MatchString(v) {
					return fmt.Errorf("user %s: %q is not a valid vault name", u.Name, v)
				}
			}
			c.Users[u.Name] = u
		}
		return nil
	}

	name := os.Getenv("SECONDBRAIN_USERNAME")
	pass := os.Getenv("SECONDBRAIN_PASSWORD")
	if name == "" && pass == "" {
		return fmt.Errorf("no users configured: set SECONDBRAIN_USERNAME and SECONDBRAIN_PASSWORD, or mount a config.yaml with a users: list")
	}
	if name == "" || pass == "" {
		return fmt.Errorf("SECONDBRAIN_USERNAME and SECONDBRAIN_PASSWORD must both be set")
	}
	resolved, err := resolveSecret(pass)
	if err != nil {
		return fmt.Errorf("SECONDBRAIN_PASSWORD: %w", err)
	}
	u := &User{Name: name, Password: resolved}
	if strings.HasPrefix(resolved, "bcrypt:") {
		u.PasswordIsHash = true
		u.Password = strings.TrimPrefix(resolved, "bcrypt:")
	}
	c.Users[name] = u
	return nil
}

func (c *Config) validate() error {
	if c.PublicURL == "" {
		return fmt.Errorf("public_url is required: set SECONDBRAIN_PUBLIC_URL to the URL clients reach this server on")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("public_url must be an absolute http or https URL")
	}
	if u.Fragment != "" || u.RawQuery != "" {
		return fmt.Errorf("public_url must not contain a query or fragment")
	}
	if c.Listen == "" {
		return fmt.Errorf("listen must not be empty")
	}
	if !filepath.IsAbs(c.DataDir) {
		return fmt.Errorf("data_dir must be an absolute path")
	}
	if !vaultNameRe.MatchString(c.DefaultVault) {
		return fmt.Errorf("default_vault %q must match %s", c.DefaultVault, vaultNameRe)
	}
	if len(c.Users) == 0 {
		return fmt.Errorf("no users configured")
	}
	for _, usr := range c.Users {
		if !usr.PasswordIsHash && len(usr.Password) < 8 {
			return fmt.Errorf("user %s: password must be at least 8 characters", usr.Name)
		}
	}
	if c.MaxResponseBytes < 4096 {
		return fmt.Errorf("max_response_bytes must be at least 4096")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setString(dst *string, env string) {
	if v := os.Getenv(env); v != "" {
		*dst = v
	}
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveSecret expands the env: and file: prefixes so that a config file can
// be committed without holding a single credential.
func resolveSecret(v string) (string, error) {
	switch {
	case strings.HasPrefix(v, "env:"):
		name := strings.TrimPrefix(v, "env:")
		got := os.Getenv(name)
		if got == "" {
			return "", fmt.Errorf("environment variable %s is empty or unset", name)
		}
		return got, nil
	case strings.HasPrefix(v, "file:"):
		p := strings.TrimPrefix(v, "file:")
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", p, err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	default:
		return v, nil
	}
}

// ---------------------------------------------------------------------------
// Rates
// ---------------------------------------------------------------------------

type Rate struct {
	Count  int
	Window time.Duration
	Source string
}

func (r Rate) Zero() bool { return r.Count == 0 }

func ParseRate(s string) (Rate, error) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 {
		return Rate{}, fmt.Errorf("expected <count>/<s|m|h>, got %q", s)
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || n <= 0 {
		return Rate{}, fmt.Errorf("invalid count in %q", s)
	}
	var w time.Duration
	switch strings.TrimSpace(parts[1]) {
	case "s":
		w = time.Second
	case "m":
		w = time.Minute
	case "h":
		w = time.Hour
	default:
		return Rate{}, fmt.Errorf("unknown unit in %q, expected s, m or h", s)
	}
	return Rate{Count: n, Window: w, Source: s}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
