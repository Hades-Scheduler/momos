// Package config loads and resolves the Momos configuration (plan.md §5): a
// single YAML file with ${ENV} substitution for secrets, defaults plus
// per-repository overrides, and glob matching for org-wide rules.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration document.
type Config struct {
	Hades        HadesConfig   `yaml:"hades"`
	Server       ServerConfig  `yaml:"server"`
	Forges       []ForgeConfig `yaml:"forges"`
	Defaults     Policy        `yaml:"defaults"`
	Repositories []RepoRule    `yaml:"repositories"`
}

// HadesConfig points at the Hades API and LogManager.
type HadesConfig struct {
	URL           string `yaml:"url"`
	AuthKey       string `yaml:"auth_key"`
	LogManagerURL string `yaml:"log_manager_url"`
}

// ServerConfig configures the Momos HTTP surface.
type ServerConfig struct {
	// Addr is the listen address for webhooks, the callback endpoint, health and metrics.
	Addr string `yaml:"addr"`
	// ExternalURL is the base URL the job containers use to reach Momos for the
	// result callback and step-start token fetch. Must be routable from the
	// Hades execution network (plan.md §11.6, §11.9).
	ExternalURL string `yaml:"external_url"`
}

// ForgeConfig configures one forge instance.
type ForgeConfig struct {
	ID            string    `yaml:"id"`
	Type          string    `yaml:"type"` // github | gitlab | gitea
	API           string    `yaml:"api"`
	WebhookSecret string    `yaml:"webhook_secret"`
	App           AppConfig `yaml:"app"`
	// Token is a PAT used for M0 (plan.md §11.5). When empty, App credentials
	// are used to mint short-lived installation tokens.
	Token string `yaml:"token"`
}

// AppConfig holds GitHub App credentials for minting installation tokens.
type AppConfig struct {
	AppID          int64  `yaml:"app_id"`
	PrivateKey     string `yaml:"private_key"`
	InstallationID int64  `yaml:"installation_id"`
}

// Policy is a fully-resolved review policy for a repository. Defaults is a
// Policy; a RepoRule overlays fields onto it (see Resolve).
type Policy struct {
	Forge      string         `yaml:"forge"`
	Prompt     string         `yaml:"prompt"`
	Priority   int            `yaml:"priority"`
	Timeout    Duration       `yaml:"timeout"`
	Triggers   Triggers       `yaml:"triggers"`
	Limits     Limits         `yaml:"limits"`
	Reviewer   ReviewerConfig `yaml:"reviewer"`
	Publish    PublishConfig  `yaml:"publish"`
	Clone      CloneConfig    `yaml:"clone"`
	ForkPolicy string         `yaml:"fork_policy"` // summary_only | full | none
}

// Triggers lists which forge actions fire a review.
type Triggers struct {
	PullRequest []string `yaml:"pull_request"`
	Push        []string `yaml:"push"`
}

// Limits bounds the work and cost of a review.
type Limits struct {
	MaxChangedFiles int     `yaml:"max_changed_files"`
	MaxDiffBytes    int     `yaml:"max_diff_bytes"`
	MaxCostUSD      float64 `yaml:"max_cost_usd"`
}

// ReviewerConfig configures the reviewer step. One image, strategy switch
// (plan.md §11.3). base_url is the single sovereignty knob (plan.md §5).
type ReviewerConfig struct {
	Strategy           string  `yaml:"strategy"` // oneshot | agentic
	Image              string  `yaml:"image"`
	Model              string  `yaml:"model"`
	BaseURL            string  `yaml:"base_url"`
	APIKey             string  `yaml:"api_key"`
	CPULimit           uint    `yaml:"cpu_limit"`
	MemoryLimit        string  `yaml:"memory_limit"`
	MaxOutputTokens    int     `yaml:"max_output_tokens"`
	MaxTurns           int     `yaml:"max_turns"` // agentic hard turn limit
	InputPricePerMTok  float64 `yaml:"input_price_per_mtok"`
	OutputPricePerMTok float64 `yaml:"output_price_per_mtok"`
}

// PublishConfig configures the publisher step.
type PublishConfig struct {
	Image          string `yaml:"image"`
	Mode           string `yaml:"mode"` // pr_review
	InlineComments bool   `yaml:"inline_comments"`
	CheckRun       bool   `yaml:"check_run"`
	// Approvals turns the model's advisory verdict into a real GitHub review
	// event: APPROVE when clean, REQUEST_CHANGES when there are problems.
	// Default off — this lets untrusted model output influence merge gating, so
	// it is opt-in, and never auto-approves a fork PR or a stale review
	// (plan.md §12.4).
	Approvals   bool   `yaml:"approvals"`
	CPULimit    uint   `yaml:"cpu_limit"`
	MemoryLimit string `yaml:"memory_limit"`
}

// CloneConfig configures the clone step (reused git-container).
type CloneConfig struct {
	Image       string `yaml:"image"`
	CPULimit    uint   `yaml:"cpu_limit"`
	MemoryLimit string `yaml:"memory_limit"`
}

// RepoRule maps a repository glob to policy overrides. Only the set fields
// override the defaults; unset (zero-value) fields inherit.
type RepoRule struct {
	Match      string            `yaml:"match"`
	Forge      string            `yaml:"forge"`
	Prompt     string            `yaml:"prompt"`
	Priority   *int              `yaml:"priority"`
	Timeout    *Duration         `yaml:"timeout"`
	Triggers   *Triggers         `yaml:"triggers"`
	Limits     *Limits           `yaml:"limits"`
	Reviewer   *ReviewerOverride `yaml:"reviewer"`
	Publish    *PublishConfig    `yaml:"publish"`
	ForkPolicy string            `yaml:"fork_policy"`
}

// ReviewerOverride overrides reviewer fields per repository (pointer fields so
// an unset value means "inherit").
type ReviewerOverride struct {
	Strategy        *string  `yaml:"strategy"`
	Image           *string  `yaml:"image"`
	Model           *string  `yaml:"model"`
	BaseURL         *string  `yaml:"base_url"`
	APIKey          *string  `yaml:"api_key"`
	MaxOutputTokens *int     `yaml:"max_output_tokens"`
	MaxTurns        *int     `yaml:"max_turns"`
	InputPrice      *float64 `yaml:"input_price_per_mtok"`
	OutputPrice     *float64 `yaml:"output_price_per_mtok"`
}

// Duration is a time.Duration that unmarshals from a YAML string like "15m".
type Duration time.Duration

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std returns the standard library duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// envRe matches ${VAR} and ${VAR:-default}.
var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// Load reads and parses the config file, substituting ${ENV} references.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

// Parse parses config bytes with ${ENV} substitution.
func Parse(raw []byte) (*Config, error) {
	substituted := envRe.ReplaceAllStringFunc(string(raw), func(m string) string {
		g := envRe.FindStringSubmatch(m)
		if v := os.Getenv(g[1]); v != "" {
			return v
		}
		return g[2] // default (empty when no ":-default" was given)
	})
	var c Config
	if err := yaml.Unmarshal([]byte(substituted), &c); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	if c.Hades.URL == "" {
		return fmt.Errorf("hades.url is required")
	}
	if c.Server.ExternalURL == "" {
		return fmt.Errorf("server.external_url is required (job containers call back to it)")
	}
	if len(c.Forges) == 0 {
		return fmt.Errorf("at least one forge is required")
	}
	for _, f := range c.Forges {
		if f.ID == "" || f.Type == "" {
			return fmt.Errorf("forge id and type are required")
		}
	}
	return nil
}

// Forge returns the forge config with the given id.
func (c *Config) Forge(id string) (ForgeConfig, bool) {
	for _, f := range c.Forges {
		if f.ID == id {
			return f, true
		}
	}
	return ForgeConfig{}, false
}

// Resolve merges the defaults with the first matching repository rule to
// produce a fully-resolved Policy for repoID.
func (c *Config) Resolve(repoID string) Policy {
	p := c.Defaults // value copy
	for _, r := range c.Repositories {
		if !matchRepo(r.Match, repoID) {
			continue
		}
		if r.Forge != "" {
			p.Forge = r.Forge
		}
		if r.Prompt != "" {
			p.Prompt = r.Prompt
		}
		if r.Priority != nil {
			p.Priority = *r.Priority
		}
		if r.Timeout != nil {
			p.Timeout = *r.Timeout
		}
		if r.Triggers != nil {
			p.Triggers = *r.Triggers
		}
		if r.Limits != nil {
			p.Limits = *r.Limits
		}
		if r.Publish != nil {
			p.Publish = *r.Publish
		}
		if r.ForkPolicy != "" {
			p.ForkPolicy = r.ForkPolicy
		}
		if r.Reviewer != nil {
			applyReviewerOverride(&p.Reviewer, r.Reviewer)
		}
		break // first match wins
	}
	return p
}

func applyReviewerOverride(base *ReviewerConfig, o *ReviewerOverride) {
	if o.Strategy != nil {
		base.Strategy = *o.Strategy
	}
	if o.Image != nil {
		base.Image = *o.Image
	}
	if o.Model != nil {
		base.Model = *o.Model
	}
	if o.BaseURL != nil {
		base.BaseURL = *o.BaseURL
	}
	if o.APIKey != nil {
		base.APIKey = *o.APIKey
	}
	if o.MaxOutputTokens != nil {
		base.MaxOutputTokens = *o.MaxOutputTokens
	}
	if o.MaxTurns != nil {
		base.MaxTurns = *o.MaxTurns
	}
	if o.InputPrice != nil {
		base.InputPricePerMTok = *o.InputPrice
	}
	if o.OutputPrice != nil {
		base.OutputPricePerMTok = *o.OutputPrice
	}
}

// matchRepo supports exact matches and a trailing "*" glob (e.g. "ls1intum/*"
// or "*"). This is enough for org-wide and prefix rules (plan.md §3③).
func matchRepo(pattern, repoID string) bool {
	if pattern == "*" || pattern == repoID {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(repoID, strings.TrimSuffix(pattern, "*"))
	}
	return false
}
