package config

import (
	"fmt"
	"regexp"
)

// Kubernetes is operator-owned execution configuration. Tasks cannot change it.
type Kubernetes struct {
	Context    string `toml:"context"`
	Kubeconfig string `toml:"kubeconfig"`
	Namespace  string `toml:"namespace"`
	Image      string `toml:"image"`
	AuthSecret string `toml:"auth_secret"`
	ProxyURL   string `toml:"proxy_url"`
}

func (k Kubernetes) Validate() error {
	name := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	if k.Context == "" || !name.MatchString(k.Namespace) || len(k.Namespace) > 63 || !name.MatchString(k.AuthSecret) || len(k.AuthSecret) > 63 {
		return fmt.Errorf("Kubernetes requires an explicit context, namespace and Codex auth_secret")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$`).MatchString(k.Image) {
		return fmt.Errorf("Kubernetes runner image must be pinned by sha256 digest")
	}
	if !regexp.MustCompile(`^http://[a-z0-9.-]+:3128$`).MatchString(k.ProxyURL) {
		return fmt.Errorf("Kubernetes requires the restricted HTTP CONNECT proxy on port 3128")
	}
	return nil
}
