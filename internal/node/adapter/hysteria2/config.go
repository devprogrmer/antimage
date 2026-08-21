package hysteria2

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/amyrm/antimage/internal/node/adapter"
	"gopkg.in/yaml.v3"
)

// ServiceParams are operator-supplied Hysteria2 service settings
type ServiceParams struct {
	Port                  int      `json:"port" yaml:"listen"`
	Password              string   `json:"password" yaml:"-"` // Handled separately
	CertFile              string   `json:"cert_file,omitempty" yaml:"-"`
	KeyFile               string   `json:"key_file,omitempty" yaml:"-"`
	SNI                   string   `json:"sni,omitempty" yaml:"-"`
	Obfs                  string   `json:"obfs,omitempty" yaml:"-"`
	ObfsPassword          string   `json:"obfs_password,omitempty" yaml:"-"`
	UpMbps                int      `json:"up_mbps,omitempty" yaml:"-"`
	DownMbps              int      `json:"down_mbps,omitempty" yaml:"-"`
	Masquerade            string   `json:"masquerade,omitempty" yaml:"-"`
	IgnoreClientBandwidth bool     `json:"ignore_client_bandwidth,omitempty" yaml:"ignoreClientBandwidth,omitempty"`
}

// Validate checks service params are well-formed
func (p ServiceParams) Validate() error {
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("port must be 1-65535, got %d", p.Port)
	}
	if len(p.Password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if p.CertFile == "" || p.KeyFile == "" {
		return fmt.Errorf("cert_file and key_file are required")
	}
	if p.Obfs == "salamander" && p.ObfsPassword == "" {
		return fmt.Errorf("obfs_password required when obfs is salamander")
	}
	if p.Obfs != "" && p.Obfs != "salamander" {
		return fmt.Errorf("obfs must be empty or 'salamander', got %q", p.Obfs)
	}
	return nil
}

// UserAuth represents a Hysteria2 user authentication entry
type UserAuth struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// GenerateConfig renders a Hysteria2 server config
func GenerateConfig(serviceID int64, params ServiceParams, users []UserAuth) (string, error) {
	if err := params.Validate(); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}

	// Sort users by username for determinism
	sorted := append([]UserAuth(nil), users...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Username < sorted[j].Username
	})

	// Build config structure
	config := map[string]interface{}{
		"listen": fmt.Sprintf(":%d", params.Port),
		"tls": map[string]string{
			"cert": params.CertFile,
			"key":  params.KeyFile,
		},
	}

	// Add auth
	if len(sorted) > 0 {
		authList := make([]map[string]string, len(sorted))
		for i, u := range sorted {
			authList[i] = map[string]string{
				"username": u.Username,
				"password": u.Password,
			}
		}
		config["auth"] = map[string]interface{}{
			"type":   "userpass",
			"userpass": authList,
		}
	} else {
		// Single password mode
		config["auth"] = map[string]interface{}{
			"type":     "password",
			"password": params.Password,
		}
	}

	// Optional: SNI
	if params.SNI != "" {
		if tls, ok := config["tls"].(map[string]string); ok {
			tls["sni"] = params.SNI
		}
	}

	// Optional: Obfuscation
	if params.Obfs == "salamander" {
		config["obfs"] = map[string]string{
			"type":     "salamander",
			"password": params.ObfsPassword,
		}
	}

	// Optional: Bandwidth
	if params.UpMbps > 0 || params.DownMbps > 0 {
		bandwidth := make(map[string]string)
		if params.UpMbps > 0 {
			bandwidth["up"] = fmt.Sprintf("%d mbps", params.UpMbps)
		}
		if params.DownMbps > 0 {
			bandwidth["down"] = fmt.Sprintf("%d mbps", params.DownMbps)
		}
		config["bandwidth"] = bandwidth
	}

	if params.IgnoreClientBandwidth {
		config["ignoreClientBandwidth"] = true
	}

	// Optional: Masquerade
	if params.Masquerade != "" {
		config["masquerade"] = map[string]interface{}{
			"type": "proxy",
			"proxy": map[string]interface{}{
				"url": params.Masquerade,
			},
		}
	}

	// Marshal to YAML
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(config); err != nil {
		return "", fmt.Errorf("encode yaml: %w", err)
	}

	body := buf.String()
	checksum := checksumContent([]byte(body))

	// Prepend marker comment
	marker := fmt.Sprintf("%s service=%d checksum=%s\n", markerPrefix, serviceID, checksum)
	return marker + body, nil
}

// UserAuthFromSubjects converts adapter subjects to Hysteria2 user auth
func UserAuthFromSubjects(subjects []adapter.Subject) []UserAuth {
	var users []UserAuth
	for _, subj := range subjects {
		// Extract password credential
		var password string
		for _, cred := range subj.Credentials {
			if cred.Kind == string(adapter.CredPassword) {
				password = cred.Value
				break
			}
		}
		if password == "" {
			continue
		}

		// Use subject ID as username
		users = append(users, UserAuth{
			Username: fmt.Sprintf("user-%d", subj.ID),
			Password: password,
		})
	}
	return users
}
