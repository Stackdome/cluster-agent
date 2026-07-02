package zotregistry

// This file mirrors the subset of the Zot registry configuration file
// (https://zotregistry.dev) that the cluster-agent generates. Each type maps to
// a JSON section of Zot's `config.json`, and the field docs explain what Zot
// does with each value — the goal is that the config is understandable without
// cross-referencing the upstream docs.
//
// Reference: https://zotregistry.dev/latest/articles/mirroring / .../retention/

// ZotConfig is the top-level Zot config file (the JSON passed as `zot serve <config>`).
type ZotConfig struct {
	// HTTP controls how the registry listens and authenticates clients.
	HTTP HTTPConfig `json:"http"`
	// Storage controls where blobs live and how garbage collection / retention run.
	Storage StorageConfig `json:"storage"`
	// Log controls log verbosity.
	Log LogConfig `json:"log"`
}

// LogConfig is the "log" section.
type LogConfig struct {
	// Level is the log verbosity: "debug", "info", "warn", "error".
	// Note: GC skip/keep decisions are only logged at "debug".
	Level string `json:"level"`
}

// HTTPConfig is the "http" section — the registry's network listener.
type HTTPConfig struct {
	// Compat enables compatibility with non-OCI clients. We set "docker2s2" so
	// the registry accepts Docker image-manifest schema-2 pushes, which is what
	// Kaniko (our in-cluster builder) and the docker CLI push. Without it those
	// pushes are rejected.
	//
	// IMPORTANT: docker2s2 means manifests are STORED in Docker schema-2 format,
	// not OCI. Zot's garbage collector only learned to collect untagged
	// docker-schema-2 manifests in v2.1.8 — older versions leak them forever and
	// the build cache grows unbounded. See config.MinZotVersion.
	Compat []string `json:"compat"`
	// Address is the bind address inside the pod ("0.0.0.0" = all interfaces).
	Address string `json:"address"`
	// Port is the container port Zot listens on (string form, e.g. "5000").
	Port string `json:"port"`
	// Auth configures client authentication. Nil = anonymous access.
	Auth *AuthConfig `json:"auth,omitempty"`
}

// AuthConfig is the "http.auth" section.
type AuthConfig struct {
	// Htpasswd enables HTTP basic auth backed by an htpasswd file.
	Htpasswd HtpasswdConfig `json:"htpasswd"`
}

// HtpasswdConfig is the "http.auth.htpasswd" section.
type HtpasswdConfig struct {
	// Path is the in-container path to the htpasswd file (mounted from a Secret).
	// Empty path disables htpasswd auth.
	Path string `json:"path"`
}

// StorageConfig is the "storage" section — the on-disk layout and the garbage
// collector that keeps it bounded.
type StorageConfig struct {
	// RootDirectory is the in-container path where Zot stores the OCI layout
	// (blobs, manifests, index.json). Backed by the registry PVC.
	RootDirectory string `json:"rootDirectory"`
	// GC enables the garbage collector. Without it nothing is ever reclaimed.
	GC bool `json:"gc"`
	// GCDelay is the grace period a blob must be unreferenced before GC removes
	// it (a zot duration, e.g. "1h"). Protects blobs from in-flight/concurrent
	// pushes; also the default value for Retention.Delay when that is unset.
	GCDelay string `json:"gcDelay"`
	// GCInterval is how often GC runs (e.g. "6h"). Shorter = tighter disk bound
	// but more GC churn; the build cache can accumulate at most one interval's
	// worth of new layers between sweeps.
	GCInterval string `json:"gcInterval"`
	// Retention configures WHICH tags/manifests survive each GC pass. GC without
	// retention only collects already-orphaned blobs; retention is what actually
	// removes tags and untagged manifests so their blobs become orphaned.
	Retention RetentionConfig `json:"retention"`
}

// RetentionConfig is the "storage.retention" section.
type RetentionConfig struct {
	// Delay is the minimum age an untagged manifest or referrer must reach before
	// deleteUntagged / deleteReferrers can remove it (a zot duration). Guards
	// against deleting manifests from an in-progress multi-step push. Zot
	// defaults this to gcDelay when omitted; we set it explicitly for clarity.
	Delay string `json:"delay,omitempty"`
	// Policies are evaluated in order and the FIRST matching policy wins for a
	// given repository (not most-specific). Consequence: repo-specific policies
	// (e.g. the cache) MUST be listed before the "**" catch-all, or they never
	// match. See buildRetentionConfig.
	Policies []RetentionPolicy `json:"policies"`
}

// RetentionPolicy is one entry in "storage.retention.policies": a set of repo
// glob patterns plus the rules for what to keep in the repos they match.
type RetentionPolicy struct {
	// Repositories is a list of glob patterns matched against the full repository
	// path. Globs use doublestar semantics: "*" matches a single path segment,
	// "**" matches across "/". Cache repos are nested (e.g.
	// "<ns>/<app>/<app>/cache"), so they need "**/cache" — a plain "*/cache"
	// matches only one segment and silently never matches.
	Repositories []string `json:"repositories"`
	// DeleteUntagged removes manifests that have no tag (e.g. an old cache entry
	// whose tag was pruned by KeepTags, or an image whose tag was overwritten),
	// once they are older than RetentionConfig.Delay. This is what reclaims the
	// bulk of build-cache space — the cache is mostly untagged manifests.
	DeleteUntagged bool `json:"deleteUntagged"`
	// DeleteReferrers removes referrer artifacts (e.g. signatures, SBOMs) whose
	// subject manifest has itself been garbage collected.
	DeleteReferrers bool `json:"deleteReferrers,omitempty"`
	// KeepTags lists the rules deciding which TAGS survive. A tag is kept if it
	// matches ANY rule in the list; every tag not kept by some rule is removed.
	// An empty/nil KeepTags means "keep all tags" (no tag pruning).
	KeepTags []KeepTagRule `json:"keepTags"`
}

// KeepTagRule is one rule within a policy's "keepTags". The fields below are
// combined with OR: a tag is retained if it satisfies ANY populated field.
type KeepTagRule struct {
	// MostRecentlyPushedCount keeps the N tags with the newest push time (per
	// repository). Bounds growth, but note it favors churny per-commit layers
	// (pushed every build) over stable layers (pushed once), so on its own it can
	// evict expensive-but-reused cache layers — pair it with PulledWithin.
	MostRecentlyPushedCount *int32 `json:"mostRecentlyPushedCount,omitempty"`
	// PulledWithin keeps any tag pulled within this zot duration (e.g. "168h").
	// For build caches this retains the working set: layers a build reuses get
	// their pull time refreshed each build and survive, while cold per-commit
	// layers that are never pulled again age out. Unlike pushedWithin it is
	// bounded by what is actually reused, not by how often builds run.
	PulledWithin string `json:"pulledWithin,omitempty"`
}
