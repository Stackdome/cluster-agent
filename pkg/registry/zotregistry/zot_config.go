package zotregistry

// ZotConfig represents the top-level structure of the Zot configuration file.
type ZotConfig struct {
	HTTP    HTTPConfig    `json:"http"`
	Storage StorageConfig `json:"storage"`
	Log     LogConfig     `json:"log"`
}

// LogConfig represents the "log" section of the Zot configuration.
type LogConfig struct {
	Level string `json:"level"`
}

// HTTPConfig represents the "http" section of the Zot configuration.
type HTTPConfig struct {
	Compat  []string   `json:"compat"`
	Address string     `json:"address"`
	Port    string     `json:"port"`
	Auth    AuthConfig `json:"auth"`
}

// AuthConfig represents the "auth" section within "http".
type AuthConfig struct {
	Htpasswd HtpasswdConfig `json:"htpasswd"`
}

// HtpasswdConfig represents the "htpasswd" section within "auth".
type HtpasswdConfig struct {
	Path string `json:"path"`
}

// StorageConfig represents the "storage" section of the Zot configuration.
type StorageConfig struct {
	RootDirectory string          `json:"rootDirectory"`
	GC            bool            `json:"gc"`
	GCDelay       string          `json:"gcDelay"`
	GCInterval    string          `json:"gcInterval"`
	Retention     RetentionConfig `json:"retention"`
}

// RetentionConfig represents the "retention" section within "storage".
type RetentionConfig struct {
	Policies []RetentionPolicy `json:"policies"`
}

// RetentionPolicy represents an individual policy within "retention.policies".
type RetentionPolicy struct {
	Repositories   []string      `json:"repositories"`
	DeleteUntagged bool          `json:"deleteUntagged"`
	KeepTags       []KeepTagRule `json:"keepTags"`
}

// KeepTagRule represents a rule within "keepTags" (e.g., mostRecentlyPushedCount).
type KeepTagRule struct {
	MostRecentlyPushedCount *int32 `json:"mostRecentlyPushedCount,omitempty"`
}
