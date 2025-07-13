package postgrescluster

// import (
// 	"context"
// 	"fmt"

// 	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
// 	. "github.com/onsi/ginkgo/v2"
// 	. "github.com/onsi/gomega"
// 	"go.uber.org/mock/gomock"
// 	corev1 "k8s.io/api/core/v1"
// 	k8sapierrors "k8s.io/apimachinery/pkg/api/errors"
// 	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
// 	"k8s.io/apimachinery/pkg/runtime"
// 	"k8s.io/apimachinery/pkg/runtime/schema"
// 	"k8s.io/utils/ptr"
// 	"sigs.k8s.io/controller-runtime/pkg/client"

// 	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
// 	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
// 	"stackdome.io/cluster-agent/internal/controller/addons/postgrescluster/mocks"
// )

// var _ = Describe("pgClusterReconciler", func() {
// 	var (
// 		mockCtrl      *gomock.Controller
// 		mockClient    *mocks.MockClient
// 		mockStatus    *mocks.MockStatusClient
// 		reconciler    *pgClusterReconciler
// 		ctx           context.Context
// 		scheme        *runtime.Scheme
// 		pgCluster     *addonsv1alpha1.PostgresCluster
// 		imageCatalog  *cnpgv1.ImageCatalog
// 		cnpgCluster   *cnpgv1.Cluster
// 	)

// 	BeforeEach(func() {
// 		mockCtrl = gomock.NewController(GinkgoT())
// 		mockClient = mocks.NewMockClient(mockCtrl)
// 		mockStatus = mocks.NewMockStatusClient(mockCtrl)
// 		ctx = context.Background()
// 		scheme = runtime.NewScheme()

// 		reconciler = &pgClusterReconciler{
// 			client: mockClient,
// 			Scheme: scheme,
// 		}

// 		pgCluster = &addonsv1alpha1.PostgresCluster{
// 			ObjectMeta: metav1.ObjectMeta{
// 				Name:      "test-postgres",
// 				Namespace: "default",
// 			},
// 			Spec: addonsv1alpha1.PostgresClusterSpec{
// 				Instances: 3,
// 				PostgreSQLSpec: &addonsv1alpha1.PostgreSQLSpec{
// 					PostgreSQLMajorVersion: 15,
// 					PostgreSQLMinorVersion: 2,
// 					ImageCatalogRef: &addonsv1alpha1.ImageCatalogRef{
// 						Name: "test-catalog",
// 					},
// 				},
// 				StorageSpec: &addonsv1alpha1.StorageSpec{
// 					StorageClassName: "fast-ssd",
// 					Size:             "10Gi",
// 				},
// 				EnableSuperuserAccess: true,
// 			},
// 		}

// 		imageCatalog = &cnpgv1.ImageCatalog{
// 			ObjectMeta: metav1.ObjectMeta{
// 				Name:      "test-catalog",
// 				Namespace: "default",
// 			},
// 			Spec: cnpgv1.ImageCatalogSpec{
// 				Images: []cnpgv1.CatalogImage{
// 					{
// 						Major: 15,
// 						Image: "postgres:15.2",
// 					},
// 				},
// 			},
// 		}

// 		cnpgCluster = &cnpgv1.Cluster{
// 			ObjectMeta: metav1.ObjectMeta{
// 				Name:      "test-postgres-15",
// 				Namespace: "default",
// 			},
// 			Spec: cnpgv1.ClusterSpec{
// 				Instances: 3,
// 				ImageName: "postgres:15.2",
// 				StorageConfiguration: cnpgv1.StorageConfiguration{
// 					StorageClass: ptr.To("fast-ssd"),
// 					Size:         "10Gi",
// 				},
// 			},
// 			Status: cnpgv1.ClusterStatus{
// 				Phase: "Cluster in healthy state",
// 				Conditions: []metav1.Condition{
// 					{
// 						Type:               string(cnpgv1.ConditionClusterReady),
// 						Status:             metav1.ConditionTrue,
// 						ObservedGeneration: 1,
// 					},
// 				},
// 				ReadService:  "test-postgres-15-r",
// 				WriteService: "test-postgres-15-rw",
// 			},
// 		}
// 		cnpgCluster.Generation = 1
// 	})

// 	AfterEach(func() {
// 		mockCtrl.Finish()
// 	})

// 	Describe("reconcile", func() {
// 		Context("when ImageCatalog is not found", func() {
// 			It("should return an error", func() {
// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-catalog",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					Return(k8sapierrors.NewNotFound(schema.GroupResource{}, "test-catalog"))

// 				result, err := reconciler.reconcile(ctx, pgCluster)

// 				Expect(err).To(HaveOccurred())
// 				Expect(result).To(Equal(resultNil))
// 			})
// 		})

// 		Context("when ImageCatalog doesn't contain the required PostgreSQL version", func() {
// 			It("should return an error", func() {
// 				catalogWithoutVersion := imageCatalog.DeepCopy()
// 				catalogWithoutVersion.Spec.Images = []cnpgv1.CatalogImage{
// 					{
// 						Major: 14,
// 						Image: "postgres:14.1",
// 					},
// 				}

// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-catalog",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj *cnpgv1.ImageCatalog, opts ...client.GetOption) error {
// 						*obj = *catalogWithoutVersion
// 						return nil
// 					})

// 				result, err := reconciler.reconcile(ctx, pgCluster)

// 				Expect(err).To(HaveOccurred())
// 				Expect(err.Error()).To(ContainSubstring("image not found in catalog"))
// 				Expect(result).To(Equal(resultNil))
// 			})
// 		})

// 		Context("when CNPG cluster doesn't exist", func() {
// 			It("should create a new cluster", func() {
// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-catalog",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj *cnpgv1.ImageCatalog, opts ...client.GetOption) error {
// 						*obj = *imageCatalog
// 						return nil
// 					})

// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-postgres-15",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					Return(k8sapierrors.NewNotFound(schema.GroupResource{}, "test-postgres-15"))

// 				mockClient.EXPECT().
// 					Create(ctx, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
// 						cluster := obj.(*cnpgv1.Cluster)
// 						Expect(cluster.Name).To(Equal("test-postgres-15"))
// 						Expect(cluster.Spec.ImageName).To(Equal("postgres:15.2"))
// 						Expect(cluster.Spec.Instances).To(Equal(int32(3)))
// 						return nil
// 					})

// 				result, err := reconciler.reconcile(ctx, pgCluster)

// 				Expect(err).NotTo(HaveOccurred())
// 				Expect(result).To(Equal(resultRequeue))
// 			})
// 		})

// 		Context("when trying to downsize storage", func() {
// 			It("should set error status and stop reconciliation", func() {
// 				existingCluster := cnpgCluster.DeepCopy()
// 				existingCluster.Spec.StorageConfiguration.Size = "20Gi"

// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-catalog",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj *cnpgv1.ImageCatalog, opts ...client.GetOption) error {
// 						*obj = *imageCatalog
// 						return nil
// 					})

// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-postgres-15",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj *cnpgv1.Cluster, opts ...client.GetOption) error {
// 						*obj = *existingCluster
// 						return nil
// 					})

// 				result, err := reconciler.reconcile(ctx, pgCluster)

// 				Expect(err).NotTo(HaveOccurred())
// 				Expect(result).To(Equal(resultStop))
// 				Expect(pgCluster.Status.Phase).To(Equal(addonsv1alpha1.ErrorPhase))
// 			})
// 		})

// 		Context("when cluster exists and is ready", func() {
// 			It("should update the cluster and set ready status", func() {
// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-catalog",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj *cnpgv1.ImageCatalog, opts ...client.GetOption) error {
// 						*obj = *imageCatalog
// 						return nil
// 					})

// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-postgres-15",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj *cnpgv1.Cluster, opts ...client.GetOption) error {
// 						*obj = *cnpgCluster
// 						return nil
// 					})

// 				mockClient.EXPECT().
// 					Patch(ctx, gomock.Any(), client.Apply, gomock.Any()).
// 					Return(nil)

// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-postgres-15",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj *cnpgv1.Cluster, opts ...client.GetOption) error {
// 						*obj = *cnpgCluster
// 						return nil
// 					})

// 				secretList := &corev1.SecretList{
// 					Items: []corev1.Secret{
// 						{
// 							ObjectMeta: metav1.ObjectMeta{
// 								Name:      "test-postgres-15-superuser",
// 								Namespace: "default",
// 							},
// 							Type: corev1.SecretTypeBasicAuth,
// 						},
// 						{
// 							ObjectMeta: metav1.ObjectMeta{
// 								Name:      "test-postgres-15-ca",
// 								Namespace: "default",
// 							},
// 						},
// 					},
// 				}

// 				mockClient.EXPECT().
// 					List(ctx, gomock.Any(), client.InNamespace("default")).
// 					DoAndReturn(func(ctx context.Context, obj *corev1.SecretList, opts ...client.ListOption) error {
// 						*obj = *secretList
// 						return nil
// 					})

// 				mockClient.EXPECT().
// 					Status().
// 					Return(mockStatus)

// 				mockStatus.EXPECT().
// 					Update(ctx, pgCluster).
// 					Return(nil)

// 				result, err := reconciler.reconcile(ctx, pgCluster)

// 				Expect(err).NotTo(HaveOccurred())
// 				Expect(result).To(Equal(resultNil))
// 				Expect(pgCluster.Status.Phase).To(Equal("Cluster in healthy state"))
// 				Expect(pgCluster.Status.CurrentImage).To(Equal("postgres:15.2"))
// 				Expect(pgCluster.Status.Outputs.ClusterName).To(Equal("test-postgres-15"))
// 			})
// 		})

// 		Context("when cluster is not ready", func() {
// 			It("should set not ready status", func() {
// 				notReadyCluster := cnpgCluster.DeepCopy()
// 				notReadyCluster.Status.Conditions[0].Status = metav1.ConditionFalse

// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-catalog",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj *cnpgv1.ImageCatalog, opts ...client.GetOption) error {
// 						*obj = *imageCatalog
// 						return nil
// 					})

// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-postgres-15",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj *cnpgv1.Cluster, opts ...client.GetOption) error {
// 						*obj = *cnpgCluster
// 						return nil
// 					})

// 				mockClient.EXPECT().
// 					Patch(ctx, gomock.Any(), client.Apply, gomock.Any()).
// 					Return(nil)

// 				mockClient.EXPECT().
// 					Get(ctx, client.ObjectKey{
// 						Name:      "test-postgres-15",
// 						Namespace: "default",
// 					}, gomock.Any()).
// 					DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj *cnpgv1.Cluster, opts ...client.GetOption) error {
// 						*obj = *notReadyCluster
// 						return nil
// 					})

// 				result, err := reconciler.reconcile(ctx, pgCluster)

// 				Expect(err).NotTo(HaveOccurred())
// 				Expect(result).To(Equal(resultStop))
// 			})
// 		})
// 	})

// 	Describe("populateStatus", func() {
// 		It("should populate status with cluster information and secrets", func() {
// 			secretList := &corev1.SecretList{
// 				Items: []corev1.Secret{
// 					{
// 						ObjectMeta: metav1.ObjectMeta{
// 							Name:      "test-postgres-15-superuser",
// 							Namespace: "default",
// 						},
// 						Type: corev1.SecretTypeBasicAuth,
// 					},
// 					{
// 						ObjectMeta: metav1.ObjectMeta{
// 							Name:      "test-postgres-15-ca",
// 							Namespace: "default",
// 						},
// 					},
// 					{
// 						ObjectMeta: metav1.ObjectMeta{
// 							Name:      "test-postgres-15-user1",
// 							Namespace: "default",
// 						},
// 						Type: corev1.SecretTypeBasicAuth,
// 					},
// 				},
// 			}

// 			mockClient.EXPECT().
// 				List(ctx, gomock.Any(), client.InNamespace("default")).
// 				DoAndReturn(func(ctx context.Context, obj *corev1.SecretList, opts ...client.ListOption) error {
// 					*obj = *secretList
// 					return nil
// 				})

// 			err := reconciler.populateStatus(ctx, pgCluster, cnpgCluster)

// 			Expect(err).NotTo(HaveOccurred())
// 			Expect(pgCluster.Status.CurrentImage).To(Equal("postgres:15.2"))
// 			Expect(pgCluster.Status.CurrentPostgreSQLVersion).To(Equal("15.2"))
// 			Expect(pgCluster.Status.Outputs.ClusterName).To(Equal("test-postgres-15"))
// 			Expect(pgCluster.Status.Outputs.ReadService).To(Equal("test-postgres-15-r"))
// 			Expect(pgCluster.Status.Outputs.WriteService).To(Equal("test-postgres-15-rw"))
// 			Expect(*pgCluster.Status.Outputs.SuperUserCredentialSecret).To(Equal("test-postgres-15-superuser"))
// 			Expect(pgCluster.Status.Outputs.ClientCASecret).To(Equal("test-postgres-15-ca"))
// 			Expect(pgCluster.Status.Outputs.UserCredentialSecrets).To(ContainElement("test-postgres-15-user1"))
// 		})

// 		It("should handle error when listing secrets fails", func() {
// 			mockClient.EXPECT().
// 				List(ctx, gomock.Any(), client.InNamespace("default")).
// 				Return(fmt.Errorf("list error"))

// 			err := reconciler.populateStatus(ctx, pgCluster, cnpgCluster)

// 			Expect(err).To(HaveOccurred())
// 			Expect(err.Error()).To(ContainSubstring("failed to list secrets"))
// 		})
// 	})

// 	Describe("buildDesiredCluster", func() {
// 		It("should build a cluster with basic configuration", func() {
// 			cluster := buildDesiredCluster(pgCluster, "postgres:15.2")

// 			Expect(cluster.Name).To(Equal("test-postgres-15"))
// 			Expect(cluster.Namespace).To(Equal("default"))
// 			Expect(cluster.Spec.Instances).To(Equal(int32(3)))
// 			Expect(cluster.Spec.ImageName).To(Equal("postgres:15.2"))
// 			Expect(*cluster.Spec.StorageConfiguration.StorageClass).To(Equal("fast-ssd"))
// 			Expect(cluster.Spec.StorageConfiguration.Size).To(Equal("10Gi"))
// 			Expect(*cluster.Spec.EnableSuperuserAccess).To(BeTrue())
// 			Expect(*cluster.Spec.EnablePDB).To(BeTrue())
// 		})

// 		It("should disable PDB for single instance cluster", func() {
// 			singleInstance := pgCluster.DeepCopy()
// 			singleInstance.Spec.Instances = 1

// 			cluster := buildDesiredCluster(singleInstance, "postgres:15.2")

// 			Expect(*cluster.Spec.EnablePDB).To(BeFalse())
// 		})

// 		It("should include bootstrap configuration when specified", func() {
// 			pgClusterWithBootstrap := pgCluster.DeepCopy()
// 			pgClusterWithBootstrap.Spec.Bootstrap = &addonsv1alpha1.BootstrapSpec{
// 				InitDb: &addonsv1alpha1.InitDbSpec{
// 					DbName:        "mydb",
// 					OwnerRoleName: "myuser",
// 				},
// 			}

// 			cluster := buildDesiredCluster(pgClusterWithBootstrap, "postgres:15.2")

// 			Expect(cluster.Spec.Bootstrap).NotTo(BeNil())
// 			Expect(cluster.Spec.Bootstrap.InitDB.Database).To(Equal("mydb"))
// 			Expect(cluster.Spec.Bootstrap.InitDB.Owner).To(Equal("myuser"))
// 		})
// 	})

// 	Describe("buildPostgresConfiguration", func() {
// 		It("should build postgres configuration with synchronous replicas", func() {
// 			pgClusterWithReplicas := pgCluster.DeepCopy()
// 			pgClusterWithReplicas.Spec.ReplicasSpec = addonsv1alpha1.ReplicasSpec{
// 				NumSynchronousReplicas:              2,
// 				SynchronousReplicaDataDurability:    cnpgv1.SynchronousReplicaDataDurabilityQuorum,
// 			}
// 			pgClusterWithReplicas.Spec.PostgreSQLSpec.PostgresConf = map[string]string{
// 				"max_connections": "200",
// 				"shared_buffers":  "256MB",
// 			}

// 			config := buildPostgresConfiguration(pgClusterWithReplicas)

// 			Expect(config.Synchronous.Method).To(Equal(cnpgv1.SynchronousReplicaConfigurationMethodAny))
// 			Expect(config.Synchronous.Number).To(Equal(int32(2)))
// 			Expect(config.Synchronous.DataDurability).To(Equal(cnpgv1.SynchronousReplicaDataDurabilityQuorum))
// 			Expect(config.Parameters).To(HaveKeyWithValue("max_connections", "200"))
// 			Expect(config.Parameters).To(HaveKeyWithValue("shared_buffers", "256MB"))
// 		})
// 	})

// 	Describe("buildPluginsSpec", func() {
// 		Context("when WAL archiving is enabled", func() {
// 			It("should return barman-cloud plugin configuration", func() {
// 				pgClusterWithBackup := pgCluster.DeepCopy()
// 				pgClusterWithBackup.Spec.ClusterBackupSpec = &addonsv1alpha1.ClusterBackupSpec{
// 					WalArchivingEnabled: true,
// 					ObjectStoreName:     "my-backup-store",
// 				}

// 				plugins := buildPluginsSpec(pgClusterWithBackup)

// 				Expect(plugins).To(HaveLen(1))
// 				Expect(plugins[0].Name).To(Equal("barman-cloud.cloudnative-pg.io"))
// 				Expect(*plugins[0].Enabled).To(BeTrue())
// 				Expect(*plugins[0].IsWALArchiver).To(BeTrue())
// 				Expect(plugins[0].Parameters["barmanObjectName"]).To(Equal("my-backup-store"))
// 			})
// 		})

// 		Context("when backup spec is nil", func() {
// 			It("should return nil", func() {
// 				plugins := buildPluginsSpec(pgCluster)
// 				Expect(plugins).To(BeNil())
// 			})
// 		})

// 		Context("when WAL archiving is disabled", func() {
// 			It("should return nil", func() {
// 				pgClusterWithBackup := pgCluster.DeepCopy()
// 				pgClusterWithBackup.Spec.ClusterBackupSpec = &addonsv1alpha1.ClusterBackupSpec{
// 					WalArchivingEnabled: false,
// 				}

// 				plugins := buildPluginsSpec(pgClusterWithBackup)
// 				Expect(plugins).To(BeNil())
// 			})
// 		})
// 	})

// 	Describe("buildExternalClusters", func() {
// 		Context("when bootstrap is nil", func() {
// 			It("should return nil", func() {
// 				clusters := buildExternalClusters(pgCluster)
// 				Expect(clusters).To(BeNil())
// 			})
// 		})

// 		Context("when bootstrap has import configuration", func() {
// 			It("should return external cluster for import", func() {
// 				pgClusterWithImport := pgCluster.DeepCopy()
// 				pgClusterWithImport.Spec.Bootstrap = &addonsv1alpha1.BootstrapSpec{
// 					InitDb: &addonsv1alpha1.InitDbSpec{
// 						Import: &addonsv1alpha1.ImportSpec{
// 							SourceClusterSpec: addonsv1alpha1.SourceClusterSpec{
// 								Name:    "source-cluster",
// 								Host:    "source.example.com",
// 								Port:    5432,
// 								User:    "postgres",
// 								DbName:  "sourcedb",
// 								SslMode: "require",
// 								Password: addonsv1alpha1.PasswordSpec{
// 									SecretRef: addonsv1alpha1.SecretRef{
// 										Name: "source-password",
// 									},
// 									Key: "password",
// 								},
// 							},
// 						},
// 					},
// 				}

// 				clusters := buildExternalClusters(pgClusterWithImport)

// 				Expect(clusters).To(HaveLen(1))
// 				Expect(clusters[0].Name).To(Equal("source-cluster"))
// 				Expect(clusters[0].ConnectionParameters["host"]).To(Equal("source.example.com"))
// 				Expect(clusters[0].ConnectionParameters["port"]).To(Equal("5432"))
// 				Expect(clusters[0].Password.Name).To(Equal("source-password"))
// 				Expect(clusters[0].Password.Key).To(Equal("password"))
// 			})
// 		})

// 		Context("when bootstrap has recovery from object store", func() {
// 			It("should return external cluster for recovery", func() {
// 				pgClusterWithRecovery := pgCluster.DeepCopy()
// 				pgClusterWithRecovery.Spec.Bootstrap = &addonsv1alpha1.BootstrapSpec{
// 					Recovery: &addonsv1alpha1.RecoverySpec{
// 						ObjectStoreSpec: &addonsv1alpha1.ObjectStoreSpec{
// 							ObjectStoreName: "backup-store",
// 						},
// 					},
// 				}

// 				clusters := buildExternalClusters(pgClusterWithRecovery)

// 				Expect(clusters).To(HaveLen(1))
// 				Expect(clusters[0].Name).To(Equal("backup-store"))
// 				Expect(clusters[0].PluginConfiguration.Name).To(Equal("barman-cloud.cloudnative-pg.io"))
// 				Expect(clusters[0].PluginConfiguration.Parameters["barmanObjectName"]).To(Equal("backup-store"))
// 			})
// 		})
// 	})

// 	Describe("buildBootstrapSpec", func() {
// 		Context("when bootstrap is nil", func() {
// 			It("should return nil", func() {
// 				bootstrap := buildBootstrapSpec(pgCluster)
// 				Expect(bootstrap).To(BeNil())
// 			})
// 		})

// 		Context("when bootstrap has InitDb configuration", func() {
// 			It("should return InitDB bootstrap configuration", func() {
// 				pgClusterWithInitDb := pgCluster.DeepCopy()
// 				pgClusterWithInitDb.Spec.Bootstrap = &addonsv1alpha1.BootstrapSpec{
// 					InitDb: &addonsv1alpha1.InitDbSpec{
// 						DbName:        "myapp",
// 						OwnerRoleName: "myuser",
// 						Password: &addonsv1alpha1.PasswordSpec{
// 							SecretRef: addonsv1alpha1.SecretRef{
// 								Name: "init-password",
// 							},
// 						},
// 					},
// 				}

// 				bootstrap := buildBootstrapSpec(pgClusterWithInitDb)

// 				Expect(bootstrap.InitDB).NotTo(BeNil())
// 				Expect(bootstrap.InitDB.Database).To(Equal("myapp"))
// 				Expect(bootstrap.InitDB.Owner).To(Equal("myuser"))
// 				Expect(bootstrap.InitDB.Secret.Name).To(Equal("init-password"))
// 			})
// 		})

// 		Context("when bootstrap has recovery from backup", func() {
// 			It("should return recovery bootstrap configuration", func() {
// 				pgClusterWithRecovery := pgCluster.DeepCopy()
// 				pgClusterWithRecovery.Spec.Bootstrap = &addonsv1alpha1.BootstrapSpec{
// 					Recovery: &addonsv1alpha1.RecoverySpec{
// 						DbName:        "recovereddb",
// 						OwnerRoleName: "recovereduser",
// 						BackupSpec: &addonsv1alpha1.BackupSpec{
// 							BackupName: "my-backup",
// 						},
// 					},
// 				}

// 				bootstrap := buildBootstrapSpec(pgClusterWithRecovery)

// 				Expect(bootstrap.Recovery).NotTo(BeNil())
// 				Expect(bootstrap.Recovery.Database).To(Equal("recovereddb"))
// 				Expect(bootstrap.Recovery.Owner).To(Equal("recovereduser"))
// 				Expect(bootstrap.Recovery.Backup.Name).To(Equal("my-backup"))
// 			})
// 		})
// 	})

// 	Describe("Name", func() {
// 		It("should return the correct reconciler name", func() {
// 			name := reconciler.Name()
// 			Expect(name).To(Equal(pgClusterReconcilerName))
// 		})
// 	})
// })
