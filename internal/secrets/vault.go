// Package secrets loads runtime configuration from HashiCorp Vault or OpenBao.
// It is used when a deployment keeps secrets outside the process environment.
package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	vault "github.com/hashicorp/vault/api"
)

// VaultConfig describes how to reach and authenticate to Vault/OpenBao.
type VaultConfig struct {
	Address             string        `mapstructure:"address"`
	Path                string        `mapstructure:"path"`
	Token               string        `mapstructure:"token"`
	AuthPath            string        `mapstructure:"auth_path"`
	Role                string        `mapstructure:"role"`
	ServiceAccountToken string        `mapstructure:"service_account_token"`
	Timeout             time.Duration `mapstructure:"timeout"`
}

// Enabled reports whether Vault is explicitly configured.
func (c VaultConfig) Enabled() bool {
	return strings.TrimSpace(c.Address) != ""
}

// ApplyVaultEnv loads the configured Vault secret and copies every key into the
// process environment when the variable is not already set. Existing values
// always win, so a developer can override a single value in .env.
func ApplyVaultEnv(ctx context.Context, cfg VaultConfig) error {
	if !cfg.Enabled() {
		return nil
	}
	values, err := cfg.Load(ctx)
	if err != nil {
		return err
	}
	for key, value := range values {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if strings.TrimSpace(os.Getenv(key)) != "" {
			continue
		}
		_ = os.Setenv(key, value)
	}
	return nil
}

// Load authenticates and returns the key/value pairs of the configured KV v2
// path. A token is used when present; otherwise the pod service account is
// exchanged through the Kubernetes auth method.
func (c VaultConfig) Load(ctx context.Context) (map[string]string, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("vault is not configured")
	}

	apiCfg := vault.DefaultConfig()
	apiCfg.Address = c.Address
	if c.Timeout > 0 {
		apiCfg.Timeout = c.Timeout
	}
	client, err := vault.NewClient(apiCfg)
	if err != nil {
		return nil, fmt.Errorf("vault client: %w", err)
	}

	switch {
	case strings.TrimSpace(c.Token) != "":
		client.SetToken(c.Token)
	case strings.TrimSpace(os.Getenv("VAULT_TOKEN")) != "" || strings.TrimSpace(os.Getenv("AGENTHUB_VAULT_TOKEN")) != "":
		client.SetToken(firstEnv("AGENTHUB_VAULT_TOKEN", "VAULT_TOKEN"))
	case strings.TrimSpace(c.Role) != "":
		if err := loginKubernetes(ctx, client, c); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("vault: set token or AGENTHUB_VAULT_ROLE")
	}

	mount, subPath := SplitKVPath(c.Path)
	secret, err := client.KVv2(mount).Get(ctx, subPath)
	if err != nil {
		return nil, fmt.Errorf("vault read %s: %w", c.Path, err)
	}
	if secret == nil {
		return nil, fmt.Errorf("vault read %s: secret not found", c.Path)
	}

	values := make(map[string]string, len(secret.Data))
	for key, value := range secret.Data {
		if text, ok := value.(string); ok {
			values[key] = text
			continue
		}
		values[key] = fmt.Sprint(value)
	}
	return values, nil
}

func loginKubernetes(ctx context.Context, client *vault.Client, c VaultConfig) error {
	jwt, err := os.ReadFile(c.ServiceAccountToken)
	if err != nil {
		return fmt.Errorf("vault kubernetes login: read service account token (%s): %w", c.ServiceAccountToken, err)
	}

	secret, err := client.Logical().WriteWithContext(ctx, "auth/"+c.AuthPath+"/login", map[string]any{
		"role": c.Role,
		"jwt":  strings.TrimSpace(string(jwt)),
	})
	if err != nil {
		return fmt.Errorf("vault kubernetes login: %w", err)
	}
	if secret == nil || secret.Auth == nil || secret.Auth.ClientToken == "" {
		return fmt.Errorf("vault kubernetes login: no client token returned")
	}
	client.SetToken(secret.Auth.ClientToken)
	return nil
}

// SplitKVPath splits a KV v2 path into its mount and the path relative to the
// data/ prefix. It accepts both "secret/data/AgentHub" and "secret/AgentHub".
func SplitKVPath(path string) (mount, subPath string) {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return "", ""
	}
	if index := strings.Index(path, "/data/"); index >= 0 {
		return path[:index], path[index+len("/data/"):]
	}
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
