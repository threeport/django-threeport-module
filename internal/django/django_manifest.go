package django

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	kube "github.com/threeport/threeport/pkg/kube/v0"

	v0 "django-threeport-module/pkg/api/v0"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// postgresImage is the database the module deploys alongside the
	// application. Django's first-party database backend is PostgreSQL, and
	// the module has no managed-database object to delegate to on the 0.7
	// line, so it always runs one in the cluster.
	postgresImage = "postgres:16-alpine"

	// djangoPort is the port gunicorn listens on inside the container. The
	// service maps 80 onto it so callers reach the app on a normal HTTP port.
	djangoPort = 8000

	// postgresPort is the standard PostgreSQL port.
	postgresPort = 5432

	// dbName and dbUser name the database the application connects to. They
	// are fixed rather than configurable: the connection details travel to the
	// app through the same secret either way, so exposing them would add API
	// surface without giving the user anything.
	dbName = "django"
	dbUser = "django"
)

// DbSecretName returns the name of the Secret holding the database credentials
// for a definition. The instance reconciler creates it and the manifest
// references it, so both have to agree on the name.
func DbSecretName(definitionName string) string {
	return fmt.Sprintf("%s-db", definitionName)
}

// AppSecretName returns the name of the Secret holding the application's own
// credentials, currently SECRET_KEY.
//
// It is kept apart from the database secret because the database secret is
// handed to the postgres container whole, through envFrom. Adding SECRET_KEY
// there would put Django's signing key in the database container's environment,
// widening the blast radius of a compromise rather than narrowing it.
func AppSecretName(definitionName string) string {
	return fmt.Sprintf("%s-app", definitionName)
}

// djangoYaml returns a YAML manifest describing a Django application: a
// PostgreSQL database with its storage and credentials, an optional migration
// job, and the application deployment and service.
//
// The whole document is handed to Threeport as one Kubernetes workload
// definition, so the pieces are created and removed together.
//
// No namespace is set on any object: Threeport assigns one per workload
// instance and rewrites whatever the manifest declares, so naming it here would
// suggest a control the module does not have.
func djangoYaml(
	definitionName string,
	image string,
	settingsModule string,
	replicas int,
	environment string,
	dbStorageGb int,
	runMigrations bool,
	env []string,
	secretEnvVars []v0.DjangoSecretEnvVar,
) (string, error) {
	var yamlDoc string

	labels := func(component string) map[string]interface{} {
		return map[string]interface{}{
			"app.kubernetes.io/name":       component,
			"app.kubernetes.io/instance":   definitionName,
			"app.kubernetes.io/managed-by": "django-threeport-module",
			"environment":                  environment,
		}
	}

	dbSecretName := DbSecretName(definitionName)
	appSecretName := AppSecretName(definitionName)
	dbServiceName := fmt.Sprintf("%s-postgres", definitionName)

	// the secret these objects reference is deliberately not created here. It
	// holds a credential that has to differ per deployment, and this document is
	// rendered once per definition and shared by every instance of it, so a
	// secret built here would hand them all the same password. The instance
	// reconciler creates it in the instance's own namespace instead, which is
	// also what makes reusing one name across instances safe.
	var err error

	dbVolumeClaim := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "PersistentVolumeClaim",
			"metadata": map[string]interface{}{
				"name":   dbServiceName,
				"labels": labels("postgres"),
			},
			"spec": map[string]interface{}{
				"accessModes": []interface{}{"ReadWriteOnce"},
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"storage": fmt.Sprintf("%dGi", dbStorageGb),
					},
				},
			},
		},
	}
	yamlDoc, err = kube.AppendObjectToYamlDoc(dbVolumeClaim, yamlDoc)
	if err != nil {
		return yamlDoc, fmt.Errorf("failed to append database volume claim to YAML manifest: %w", err)
	}

	dbDeployment := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":   dbServiceName,
				"labels": labels("postgres"),
			},
			"spec": map[string]interface{}{
				// one replica only: this is a single volume with a single
				// writer, not a replicated database
				"replicas": 1,
				"selector": map[string]interface{}{
					"matchLabels": labels("postgres"),
				},
				"strategy": map[string]interface{}{
					// the volume cannot be mounted by two pods at once, so the
					// old pod has to go before the new one starts
					"type": "Recreate",
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": labels("postgres"),
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "postgres",
								"image": postgresImage,
								"ports": []interface{}{
									map[string]interface{}{"containerPort": postgresPort},
								},
								"envFrom": []interface{}{
									map[string]interface{}{
										"secretRef": map[string]interface{}{"name": dbSecretName},
									},
								},
								"volumeMounts": []interface{}{
									map[string]interface{}{
										"name":      "data",
										"mountPath": "/var/lib/postgresql/data",
										// postgres refuses to initialise into a
										// directory that is not empty, and a
										// mounted volume root carries lost+found
										"subPath": "pgdata",
									},
								},
								"readinessProbe": map[string]interface{}{
									"exec": map[string]interface{}{
										"command": []interface{}{"pg_isready", "-U", dbUser, "-d", dbName},
									},
									"initialDelaySeconds": 5,
									"periodSeconds":       5,
								},
							},
						},
						"volumes": []interface{}{
							map[string]interface{}{
								"name": "data",
								"persistentVolumeClaim": map[string]interface{}{
									"claimName": dbServiceName,
								},
							},
						},
					},
				},
			},
		},
	}
	yamlDoc, err = kube.AppendObjectToYamlDoc(dbDeployment, yamlDoc)
	if err != nil {
		return yamlDoc, fmt.Errorf("failed to append database deployment to YAML manifest: %w", err)
	}

	dbService := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name":   dbServiceName,
				"labels": labels("postgres"),
			},
			"spec": map[string]interface{}{
				"selector": labels("postgres"),
				"ports": []interface{}{
					map[string]interface{}{
						"port":       postgresPort,
						"targetPort": postgresPort,
					},
				},
			},
		},
	}
	yamlDoc, err = kube.AppendObjectToYamlDoc(dbService, yamlDoc)
	if err != nil {
		return yamlDoc, fmt.Errorf("failed to append database service to YAML manifest: %w", err)
	}

	// the application's environment: the database connection comes from the
	// same secret postgres was configured with, so the two cannot drift, and
	// the signing key from a secret of its own
	appEnv := []interface{}{
		map[string]interface{}{
			"name": "DATABASE_URL",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": dbSecretName,
					"key":  "DATABASE_URL",
				},
			},
		},
		// Django refuses to load its settings without a SECRET_KEY, so the
		// migration job needs it as much as the application does. Both read it
		// from the same secret, which the instance reconciler writes once.
		map[string]interface{}{
			"name": "SECRET_KEY",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": appSecretName,
					"key":  "SECRET_KEY",
				},
			},
		},
	}
	if settingsModule != "" {
		appEnv = append(appEnv, map[string]interface{}{
			"name":  "DJANGO_SETTINGS_MODULE",
			"value": settingsModule,
		})
	}

	// user-supplied variables go last so they read after the module's own
	// wiring; reserved names are rejected at the config layer
	appEnv = append(appEnv, customEnvEntries(env, secretEnvVars)...)

	// django-admin is an installed console script, so Python puts its own
	// directory on sys.path and not the project's. A server like gunicorn adds
	// the working directory itself, which is why the application starts and a
	// migration run would not: without this the job fails with
	// ModuleNotFoundError on the settings module. The image is expected to have
	// its project at the working directory, which is the usual layout.
	migrateEnv := append([]interface{}{
		map[string]interface{}{"name": "PYTHONPATH", "value": "."},
	}, appEnv...)

	if runMigrations {
		migrationJob := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "batch/v1",
				"kind":       "Job",
				"metadata": map[string]interface{}{
					"name":   fmt.Sprintf("%s-migrate", definitionName),
					"labels": labels("django-migrate"),
				},
				"spec": map[string]interface{}{
					// a failed migration is not something to retry blindly:
					// surface it rather than loop against a broken schema
					"backoffLimit": 1,
					"template": map[string]interface{}{
						"metadata": map[string]interface{}{
							"labels": labels("django-migrate"),
						},
						"spec": map[string]interface{}{
							"restartPolicy": "Never",
							// the job and the database start together, so the
							// first migration attempt would otherwise race the
							// database and fail with connection refused. Waiting
							// here rather than raising backoffLimit keeps a
							// genuine migration failure from being retried.
							"initContainers": []interface{}{
								map[string]interface{}{
									"name":  "wait-for-database",
									"image": postgresImage,
									"command": []interface{}{
										"sh", "-c",
										fmt.Sprintf(
											"until pg_isready -h %s -p %d -U %s; do sleep 2; done",
											dbServiceName, postgresPort, dbUser,
										),
									},
								},
							},
							"containers": []interface{}{
								map[string]interface{}{
									"name":    "migrate",
									"image":   image,
									"command": []interface{}{"django-admin", "migrate", "--no-input"},
									"env":     migrateEnv,
								},
							},
						},
					},
				},
			},
		}
		yamlDoc, err = kube.AppendObjectToYamlDoc(migrationJob, yamlDoc)
		if err != nil {
			return yamlDoc, fmt.Errorf("failed to append migration job to YAML manifest: %w", err)
		}
	}

	appDeployment := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":   definitionName,
				"labels": labels("django"),
			},
			"spec": map[string]interface{}{
				"replicas": replicas,
				"selector": map[string]interface{}{
					"matchLabels": labels("django"),
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": labels("django"),
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "django",
								"image": image,
								"ports": []interface{}{
									map[string]interface{}{"containerPort": djangoPort},
								},
								"env": appEnv,
								"readinessProbe": map[string]interface{}{
									"tcpSocket": map[string]interface{}{
										"port": djangoPort,
									},
									"initialDelaySeconds": 5,
									"periodSeconds":       10,
								},
							},
						},
					},
				},
			},
		},
	}
	yamlDoc, err = kube.AppendObjectToYamlDoc(appDeployment, yamlDoc)
	if err != nil {
		return yamlDoc, fmt.Errorf("failed to append application deployment to YAML manifest: %w", err)
	}

	appService := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name":   definitionName,
				"labels": labels("django"),
			},
			"spec": map[string]interface{}{
				"selector": labels("django"),
				"ports": []interface{}{
					map[string]interface{}{
						"port":       80,
						"targetPort": djangoPort,
					},
				},
			},
		},
	}
	yamlDoc, err = kube.AppendObjectToYamlDoc(appService, yamlDoc)
	if err != nil {
		return yamlDoc, fmt.Errorf("failed to append application service to YAML manifest: %w", err)
	}

	return yamlDoc, nil
}

// generatePassword returns a URL-safe random password.
//
// crypto/rand rather than threeport's util.RandomAlphaNumericString: that
// helper seeds math/rand from the clock, so the value it produces is
// recoverable from the time the object was created. That is acceptable for a
// name suffix and not for a credential.
func generatePassword(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to read random bytes: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}
