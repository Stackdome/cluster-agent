package v1alpha1

import (
	"fmt"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

type PgExtension string

const (
	// Pgvector is a PostgreSQL extension for vector data types and operations.
	Pgvector PgExtension = "vector"
)

type DataDurability = cnpgv1.DataDurabilityLevel

type InstancePlacementPolicy string

const (
	// StrictPlacementPolicy ensures that instances are placed on strictly enforced.
	StrictPlacementPolicy InstancePlacementPolicy = "required"
	// SoftPlacementPolicy allows instances to be placed on any node, but prefers to spread them across nodes.
	SoftPlacementPolicy InstancePlacementPolicy = "preferred"
)

const (
	// Refer: https://cloudnative-pg.io/documentation/1.26/replication/#data-durability-and-synchronous-replication
	PreferredDataDurability DataDurability = cnpgv1.DataDurabilityLevelPreferred
	// In this mode a tx is committed in the primary only if its successfully replicated to the specified number of synchronous replicas.(IE ack received from the replicas)
	RequiredDataDurability DataDurability = cnpgv1.DataDurabilityLevelRequired
)

type PostgresClusterConditionType string

// Phases
const (
	PendingPhase    = "Pending"
	ErrorPhase      = "Error"
	ReadyPhase      = "Ready"
	DeletingPhase   = "Deleting"
	HibernatedPhase = "Hibernated"
)

const (
	// Condition ClusterConfigurationValidConditionType indicates whether the cluster configuration is valid.
	// This condition is set to true when the Postgres cluster configuration is valid and ready for
	// deployment.
	ClusterConfigurationValid     PostgresClusterConditionType = "ClusterConfigurationValid"
	ClusterReady                  PostgresClusterConditionType = "ClusterReady"
	ClusterHiberated              PostgresClusterConditionType = "ClusterHibernated"
	ClusterFenced                 PostgresClusterConditionType = "ClusterFenced"
	DatabasesApplied              PostgresClusterConditionType = "DatabasesApplied"
	ContinuousWalArchivingSuccess PostgresClusterConditionType = "ContinuousWalArchivingSuccess"
	LastBaseBackupSucceeded       PostgresClusterConditionType = "LastBaseBackupSucceeded"
)

// PostgresClusterSpec defines the desired state of PostgresCluster.
// +kubebuilder:validation:XValidation:rule="self.replicasSpec.numSynchronousReplicas == 0 || self.replicasSpec.numSynchronousReplicas < self.instances",message="numSynchronousReplicas must be less than the number of instances"
type PostgresClusterSpec struct {
	// Size is the number of Postgres instances in the cluster.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	Instances int `json:"instances"`
	// ReplicasSpec defines the number of synchronous replicas and their configuration.
	// +optional
	ReplicasSpec *ReplicasSpec `json:"replicasSpec,omitempty"`
	// PostgreSQL version and image configuration
	// +kubebuilder:validation:Required
	PostgreSQLSpec *PostgreSQLSpec `json:"postgreSQLSpec"`
	// Bootstrap is used to specify the bootstrap configuration for the Postgres cluster.
	// +Optional
	BootstrapSpec *BootstrapSpec `json:"bootstrapSpec,omitempty"`
	// EnableSuperuserAccess indicates whether superuser access should be enabled for the Postgres cluster.
	// +optional
	EnableSuperuserAccess bool `json:"enableSuperuserAccess,omitempty"`
	// StorageSpec defines the storage configuration for the Postgres cluster.
	// +kubebuilder:validation:Required
	StorageSpec *StorageSpec `json:"storageSpec"`
	// +optional
	// ResourceSpec defines the resource requirements for the Postgres cluster.
	ResourceSpec corev1.ResourceRequirements `json:"resourceSpec"`
	// InstancePlacementSpec defines the placement of the Postgres instances.
	// +optional
	InstancePlacementSpec *InstancePlacementSpec `json:"instancePlacementSpec,omitempty"`
	// +optional
	// ClusterBackupSpec is used to specify the cluster backup configuration for the Postgres cluster.
	ClusterBackupSpec *ClusterBackupSpec `json:"clusterBackupSpec,omitempty"`
	//+ optional
	HibernationEnabled bool `json:"hibernationEnabled,omitempty"`
	// +optional
	// FencingSpec is used to specify the fencing configuration for the Postgres cluster.
	FencingSpec *FencingSpec `json:"fencingSpec,omitempty"`
	// +optional
	// Databases is a list of databases to be created in the Postgres cluster.
	Databases []DatabaseSpec `json:"databases,omitempty"`
}

type InstancePlacementSpec struct {
	// TopologyKey is the key used to define the topology for the Postgres instances.
	// This is used to ensure that the Postgres instances are spread across different zones or regions.
	// +kubebuilder:validation:Required
	// +kubebuilder:default="kubernetes.io/hostname"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="topologyKey is immutable"
	TopologyKey string `json:"topologyKey"`
	// PlacementPolicy defines the placement policy for the Postgres instances.
	// +kubebuilder:validation:Required
	// +kubebuilder:default="preferred"
	// +kubebuilder:validation:Enum=preferred;required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="placementPolicy is immutable"
	PlacementPolicy InstancePlacementPolicy `json:"placementPolicy"`
	// Tolerations are the tolerations to be applied to the Postgres instances.
	// This is used to ensure that the Postgres instances can be scheduled on nodes with specific taints.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="tolerations is immutable"
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// NodeSelector is used to specify the node selector for the Postgres instances.
	// This is used to ensure that the Postgres instances are scheduled on specific nodes.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="nodeSelector is immutable"
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

type DatabaseSpec struct {
	// Name is the name of the database to be created.
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// Extensions is a list of extensions to be created in the database.
	Extensions []ExtensionSpec `json:"extensions,omitempty"`
}

type ExtensionSpec struct {
	// Name is the name of the extension to be created.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=vector
	Name PgExtension `json:"name"`
}

type FencingSpec struct {
	// FencingEnabled indicates whether fencing is enabled for the Postgres cluster.
	FenceCluster bool `json:"fenceCluster,omitempty"`
}

type ReplicasSpec struct {
	// Number of synchronous replicas.
	// +kubebuilder:validation:Required
	// This defines how many replicas should be kept in sync with the primary instance.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=4
	// +kubebuilder:default=0
	NumSynchronousReplicas int `json:"numSynchronousReplicas"`

	// DataDurability defines the data durability level for the Postgres cluster
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=preferred;required
	// +kubebuilder:default=preferred
	SynchronousReplicaDataDurability DataDurability `json:"dataDurability"`
}

type PostgreSQLSpec struct {
	// ImageCatalogRef references the ImageCatalog to use for this cluster
	// +kubebuilder:validation:Required
	ImageCatalogRef *ImageCatalogRef `json:"imageCatalogRef"`
	// PostgreSQLVersion specifies the major version of PostgreSQL to run
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=13
	// +kubebuilder:validation:Maximum=17
	PostgreSQLMajorVersion int `json:"postgreSQLVersion"`
	// +kubebuilder:validation:Required
	PostgreSQLMinorVersion int `json:"postgreSQLMinorVersion,omitempty"`
	// EnableMinorVersionUpgrades indicates whether automatic minor version upgrades are allowed
	// +kubebuilder:default=true
	EnableMinorVersionUpgrades bool `json:"enableMinorVersionUpgrades,omitempty"`
	// EnableMajorVersionUpgrades indicates whether automatic major version upgrades are allowed
	// +kubebuilder:default=false
	EnableMajorVersionUpgrades bool `json:"enableMajorVersionUpgrades,omitempty"`
	// PostgresConf contains the PostgreSQL configuration parameters to be applied to the cluster.
	// This is a map of parameter names to their values.
	// +optional
	PostgresConf map[string]string `json:"postgresConf,omitempty"`
}

type ImageCatalogRef struct {
	// Name of the ImageCatalog resource
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// +kubebuilder:validation:XValidation:rule="!self.walArchivingEnabled || self.scheduledBaseBackupSpec != null && self.scheduledBaseBackupSpec.enabled == true",message="scheduledBaseBackupSpec.enabled must be true when walArchivingEnabled is true"
type ClusterBackupSpec struct {
	// WalArchivingEnabled indicates whether WAL archiving should be enabled for the Postgres cluster.
	// Used for point-in-time recovery.
	// +kubebuilder:validation:Required
	WalArchivingEnabled bool `json:"walArchivingEnabled"`
	// ObjectStoreName is the name of the object store where the backup files should be stored.
	// Assumes the cnpg object store is already created in the cluster.
	// +kubebuilder:validation:Required
	ObjectStoreName string `json:"objectStoreName"`
	// +optional
	ScheduledBaseBackupSpec *ScheduledBaseBackupSpec `json:"scheduledBaseBackupSpec"`
	// +optional
	// LastBackupRequestedAt is the timestamp when the last backup was requested.
	// This is used to determine if a backup should be taken immediately.
	LastBaseBackupRequestedAt *metav1.Time `json:"lastBaseBackupRequestedAt,omitempty"`
}

// ScheduledBaseBackupSpec defines the scheduled base backup configuration for the Postgres cluster.
type ScheduledBaseBackupSpec struct {
	// +kubebuilder:validation:Required
	// Defaults to "0 0 0 * * 0" (every Sunday at midnight).
	// +kubebuilder:default="0 0 0 * * 0"
	Schedule string `json:"schedule"`
	// +kubebuilder:validation:Required
	// Defaults to true, if set to false, the base backup will not be taken.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
}

// StorageSpec defines the storage configuration for the base backup.
type StorageSpec struct {
	// StorageClassName is the name of the storage class to use for the Postgres cluster
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="StorageClassName is immutable"
	StorageClassName string `json:"storageClassName"`
	// Size is the size of the storage to use for the Postgres cluster
	// kubebuilder:validation:Pattern=`^[0-9]+[KMGTP]i?$`
	// +kubebuilder:validation:Required
	Size string `json:"size"`
}

type BootstrapSpec struct {
	InitDbSpec   *InitDbSpec   `json:"initDbSpec,omitempty"`
	RecoverySpec *RecoverySpec `json:"recoverySpec,omitempty"`
}

type InitDbSpec struct {
	// +optional
	// Import is used to import data into the Postgres cluster.
	Import *ImportSpec `json:"import"`
}

// one of ObjectStoreSpec or BackupSpec must be specified
// +kubebuilder:validation:XValidation:rule="(has(self.objectStoreSpec) && !has(self.backupSpec)) || (!has(self.objectStoreSpec) && has(self.backupSpec))",message="exactly one of objectStoreSpec or backupSpec must be specified"
// +kubebuilder:validation:Required
// RecoverySpec defines the recovery specification for the Postgres cluster.
type RecoverySpec struct {
	// +optional
	ObjectStoreSpec *RecoveryFromObjectStoreSpec `json:"objectStoreSpec,omitempty"`
	// +optional
	BackupSpec *BackupSpec `json:"backupSpec,omitempty"`
}

type RecoveryTargetSpec struct {
	// RecoveryTargetName is the name of the recovery target.
	// This is used to specify the point in time to which the Postgres cluster should be
	// recovered.
	// +kubebuilder:validation:Required
	RecoveryTargetTime string `json:"recoveryTargetTime"`
}

type RecoveryFromObjectStoreSpec struct {
	// Name of the barman object store where the backups are stored.
	// +kubebuilder:validation:Required
	ObjectStoreName string `json:"objectStoreName"`
	// SourceClusterName is the name of the source Postgres cluster from which the data was stored in the object store.
	// +kubebuilder:validation:Required
	SourceClusterName string `json:"sourceClusterName"`
	// RecoveryTarget is used to specify the point in time to which the Postgres cluster should be recovered.
	// +optional
	RecoveryTarget *RecoveryTargetSpec `json:"recoveryTarget,omitempty"`
}

type BackupSpec struct {
	// Name of the cnpg backup to restore from.
	// +kubebuilder:validation:Required
	BackupName string `json:"backupName"`
}

type ImportSpec struct {
	// List of databases to import from the source.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	Databases []string `json:"databases"`
	// SourceClusterSpec is used to specify the source cluster from which the data should be imported.
	// +kubebuilder:validation:Required
	SourceClusterSpec *ExternalClusterSpec `json:"sourceClusterSpec"`
}

type ExternalClusterSpec struct {
	//+kubebuilder:validation:Required
	// Name is the name of the external Postgres cluster.
	Name string `json:"name"`
	//+kubebuilder:validation:Required
	// Host is the hostname of the external Postgres cluster.
	Host string `json:"host"`
	//+kubebuilder:validation:Required
	// User is the username to connect to the external Postgres cluster.
	User string `json:"user"`
	// Port is the port of the external Postgres cluster.
	//+kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=5432
	Port int `json:"port"`
	//+kubebuilder:validation:Required
	// DbName is the name of the database to connect to in the external Postgres cluster.
	DbName string `json:"dbName"`
	// SslMode is the SSL mode to use when connecting to the external Postgres cluster.
	// +kubebuilder:validation:Enum="disable";"require";"verify-ca";"verify-full"
	SslMode string `json:"sslMode,omitempty"`
	// +kubebuilder:validation:Required
	Password corev1alpha1.CredentialSecret `json:"password,omitempty"`
}

func (e *ExternalClusterSpec) DbConnectionString(password string) string {
	return fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=%s password=%s",
		e.Host, e.Port, e.User, e.DbName, e.SslMode, password)
}

// PostgresClusterStatus defines the observed state of PostgresCluster.
type PostgresClusterStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +kubebuilder:default=Pending
	Phase string `json:"phase,omitempty"`
	// +optional
	StatusHash string `json:"statusHash,omitempty"`
	// +optional
	CurrentPostgreSQLMajorVersion string `json:"currentPostgreSQLMajorVersion,omitempty"`
	// +optional
	CurrentPostgreSQLMinorVersion string `json:"currentPostgreSQLMinorVersion,omitempty"`
	// CurrentImage is the image currently used by the Postgres cluster.
	// +optional
	CurrentImage string `json:"currentImage,omitempty"`
	// +optional
	Outputs *PostgresClusterOutputs `json:"outputs,omitempty"`
	// +optional
	LastImmeadiateBackupProcessedAt *metav1.Time `json:"lastImmeadiateBackupProcessedAt,omitempty"`
}

type PostgresClusterOutputs struct {
	// +optional
	ClusterName string `json:"clusterName,omitempty"`
	// +optional
	// WriteService is the service used to access the primary instance.
	WriteService string `json:"writeService,omitempty"`
	// +optional
	// ReadService is the service used to access the read replicas.
	ReadService string `json:"readService,omitempty"`
	// ClientCASecret is the name of the secret containing the CA certificate used for clients to
	// connect to the Postgres cluster.
	ClientCASecret string `json:"clientCASecretName,omitempty"`
	// +optional
	ClusterConnection *ClusterConnectionInfo `json:"clusterConnection,omitempty"`
	// +optional
	Databases []DatabaseInfo `json:"databases,omitempty"`
	// +optional
	// SuperUserCredentials contains the credentials for the superuser of the Postgres cluster.
	SuperUserCredentialSecret *string `json:"superUserCredentialSecretName,omitempty"`
	// UserCredentials contains the credentials for the users of the Postgres cluster.
	// Map owner role name to the secret name containing the credentials.
	// +optional
	UserCredentialSecrets map[string]string `json:"userCredentialSecretNames,omitempty"`
}

type DatabaseInfo struct {
	// Name is the name of the database.
	Name string `json:"name,omitempty"`
	// Owner is the owner of the database.
	Owner string `json:"owner,omitempty"`
}

type ClusterConnectionInfo struct {
	Host    string `json:"host"`
	Port    int32  `json:"port"`
	SSLMode string `json:"sslMode,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// PostgresCluster is the Schema for the postgresclusters API.
type PostgresCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PostgresClusterSpec   `json:"spec,omitempty"`
	Status PostgresClusterStatus `json:"status,omitempty"`
}

func (p *PostgresCluster) CnpgClusterName() string {
	return fmt.Sprintf("%s-%d", p.Name, p.Spec.PostgreSQLSpec.PostgreSQLMajorVersion)
}

// +kubebuilder:object:root=true

// PostgresClusterList contains a list of PostgresCluster.
type PostgresClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PostgresCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PostgresCluster{}, &PostgresClusterList{})
}
