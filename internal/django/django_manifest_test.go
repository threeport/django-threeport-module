package django

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// kindsIn returns the kind of every document in a multi-document YAML string,
// in order. Splitting on the separator is enough here because the manifest is
// built from structured objects, so no document body can contain one.
func kindsIn(t *testing.T, doc string) []string {
	t.Helper()

	var kinds []string
	for _, chunk := range strings.Split(doc, "\n---\n") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		var parsed struct {
			Kind string `json:"kind"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(chunk), &parsed), "every document must be valid YAML")
		kinds = append(kinds, parsed.Kind)
	}

	return kinds
}

// TestDjangoYaml_Resources covers the shape of a default deployment: a
// database with its storage and credentials, the migration job, and the
// application itself.
func TestDjangoYaml_Resources(t *testing.T) {
	doc, err := djangoYaml("myapp", "myorg/myapp:v1", "myapp.settings.production", 2, "dev", 20, true, nil, nil)
	require.NoError(t, err)

	kinds := kindsIn(t, doc)

	assert.NotContains(
		t, kinds, "Secret",
		"the credential is created per instance by the reconciler, not rendered into the shared manifest",
	)
	assert.Contains(t, kinds, "PersistentVolumeClaim")
	assert.Contains(t, kinds, "Job", "migrations were requested")
	assert.Contains(t, kinds, "Service")
	assert.Equal(t, 2, countOf(kinds, "Deployment"), "one for postgres and one for the app")
	assert.Equal(t, 2, countOf(kinds, "Service"), "one for postgres and one for the app")
}

// TestDjangoYaml_MigrationsDisabled covers RunMigrations being false: the job
// is the only resource that should disappear.
func TestDjangoYaml_MigrationsDisabled(t *testing.T) {
	doc, err := djangoYaml("myapp", "myorg/myapp:v1", "", 1, "dev", 20, false, nil, nil)
	require.NoError(t, err)

	kinds := kindsIn(t, doc)

	assert.NotContains(t, kinds, "Job", "no migration job when migrations are turned off")
	assert.Contains(t, kinds, "Deployment", "the application is still deployed")
}

// TestDjangoYaml_SettingsModuleOmitted covers the optional settings module.
// Django falls back to its own default when the variable is absent, so an
// empty value must not be set rather than set to "".
func TestDjangoYaml_SettingsModuleOmitted(t *testing.T) {
	withSettings, err := djangoYaml("myapp", "myorg/myapp:v1", "myapp.settings.production", 1, "dev", 20, false, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, withSettings, "DJANGO_SETTINGS_MODULE")

	withoutSettings, err := djangoYaml("myapp", "myorg/myapp:v1", "", 1, "dev", 20, false, nil, nil)
	require.NoError(t, err)
	assert.NotContains(t, withoutSettings, "DJANGO_SETTINGS_MODULE")
}

// TestDjangoYaml_ReferencesTheInstanceSecret covers the contract between the
// manifest and the instance reconciler: the manifest names a secret it does not
// create, so both sides have to derive the same name.
func TestDjangoYaml_ReferencesTheInstanceSecret(t *testing.T) {
	doc, err := djangoYaml("myapp", "myorg/myapp:v1", "myapp.settings", 1, "dev", 20, false, nil, nil)
	require.NoError(t, err)

	assert.Contains(t, doc, DbSecretName("myapp"), "the deployments have to reference the name the reconciler creates")
	assert.Contains(t, doc, AppSecretName("myapp"), "the deployments have to reference the name the reconciler creates")
}

// TestDjangoYaml_SecretKeyReachesTheApplicationAndMigrations covers what the
// manifest wires SECRET_KEY into. Django will not load its settings without
// one, so the migration job needs it as much as the application does.
func TestDjangoYaml_SecretKeyReachesTheApplicationAndMigrations(t *testing.T) {
	doc, err := djangoYaml("myapp", "myorg/myapp:v1", "myapp.settings", 1, "dev", 20, true, nil, nil)
	require.NoError(t, err)

	for _, container := range []string{"django", "migrate"} {
		name, key := secretKeyRefFor(t, doc, container, "SECRET_KEY")
		assert.Equal(t, AppSecretName("myapp"), name, "container %q reads SECRET_KEY from the wrong secret", container)
		assert.Equal(t, "SECRET_KEY", key, "container %q reads the wrong key", container)
	}

	// the database secret is handed to postgres whole through envFrom, so a
	// signing key placed there would end up in the database container too
	postgresName, _ := secretKeyRefFor(t, doc, "postgres", "SECRET_KEY")
	assert.Empty(t, postgresName, "the database container has no business holding the signing key")
}

// secretKeyRefFor returns the secret name and key a named container reads an
// environment variable from, or empty strings when it does not read that
// variable at all.
func secretKeyRefFor(t *testing.T, doc string, containerName string, variable string) (string, string) {
	t.Helper()

	for _, chunk := range strings.Split(doc, "\n---\n") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		var parsed struct {
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name string `json:"name"`
							Env  []struct {
								Name      string `json:"name"`
								ValueFrom struct {
									SecretKeyRef struct {
										Name string `json:"name"`
										Key  string `json:"key"`
									} `json:"secretKeyRef"`
								} `json:"valueFrom"`
							} `json:"env"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(chunk), &parsed))

		for _, container := range parsed.Spec.Template.Spec.Containers {
			if container.Name != containerName {
				continue
			}
			for _, env := range container.Env {
				if env.Name == variable {
					return env.ValueFrom.SecretKeyRef.Name, env.ValueFrom.SecretKeyRef.Key
				}
			}
		}
	}

	return "", ""
}

// TestApplicationSecretData covers the signing key the reconciler writes. A key
// shared between instances means a token minted by one is accepted by all the
// others, which is the whole reason it cannot live on the definition.
func TestApplicationSecretData(t *testing.T) {
	data, err := applicationSecretData()
	require.NoError(t, err)

	assert.NotEmpty(t, data["SECRET_KEY"])
	assert.GreaterOrEqual(
		t, len(data["SECRET_KEY"]), 50,
		"django's own get_random_secret_key produces 50 characters; do not go below it",
	)

	other, err := applicationSecretData()
	require.NoError(t, err)
	assert.NotEqual(
		t, data["SECRET_KEY"], other["SECRET_KEY"],
		"two instances of one definition must not share a signing key",
	)
}

// TestDatabaseSecretData covers the credential the reconciler writes. The
// PostgreSQL deployment and the application read the same secret, so the
// password in DATABASE_URL has to be the one POSTGRES_PASSWORD sets.
func TestDatabaseSecretData(t *testing.T) {
	data, err := databaseSecretData("myapp")
	require.NoError(t, err)

	assert.Equal(t, dbName, data["POSTGRES_DB"])
	assert.Equal(t, dbUser, data["POSTGRES_USER"])
	assert.NotEmpty(t, data["POSTGRES_PASSWORD"])
	assert.Contains(
		t, data["DATABASE_URL"], data["POSTGRES_PASSWORD"],
		"the connection string has to carry the password the database is configured with",
	)
	assert.Contains(t, data["DATABASE_URL"], "myapp-postgres", "it has to point at this definition's database service")

	other, err := databaseSecretData("myapp")
	require.NoError(t, err)
	assert.NotEqual(
		t, data["POSTGRES_PASSWORD"], other["POSTGRES_PASSWORD"],
		"two instances of one definition must not share a password",
	)
}

// TestGeneratePassword covers the credential the database and the application
// share. A predictable value here would be recoverable by anyone who knows
// roughly when the definition was created.
func TestGeneratePassword(t *testing.T) {
	first, err := generatePassword(32)
	require.NoError(t, err)
	second, err := generatePassword(32)
	require.NoError(t, err)

	assert.NotEmpty(t, first)
	assert.NotEqual(t, first, second, "two passwords generated in the same instant must differ")
	assert.NotContains(t, first, "=", "the encoding must be URL-safe and unpadded")
}

func countOf(items []string, want string) int {
	var n int
	for _, item := range items {
		if item == want {
			n++
		}
	}

	return n
}

// TestReplicasByEnv covers the default replica count. Only prod is special;
// anything else, including an unrecognised value, gets the single-replica
// default rather than an error, because Environment is a free-form string.
func TestReplicasByEnv(t *testing.T) {
	assert.Equal(t, 3, replicasByEnv("prod"))
	assert.Equal(t, 1, replicasByEnv("dev"))
	assert.Equal(t, 1, replicasByEnv("staging"), "an unrecognised environment falls back rather than failing")
}

// TestDbStorageByEnv covers the database volume size per environment.
func TestDbStorageByEnv(t *testing.T) {
	assert.Equal(t, 100, dbStorageByEnv("prod"))
	assert.Equal(t, 20, dbStorageByEnv("dev"))
	assert.Equal(t, 20, dbStorageByEnv(""))
}

// TestDjangoYaml_MigrationJobPythonPath covers the defect where the migration
// job failed with ModuleNotFoundError while the application started fine.
// django-admin is an installed console script, so Python puts /usr/local/bin on
// sys.path rather than the project directory; a server adds the working
// directory itself, which is why only the job broke.
func TestDjangoYaml_MigrationJobPythonPath(t *testing.T) {
	doc, err := djangoYaml("myapp", "myorg/myapp:v1", "myapp.settings", 1, "dev", 20, true, nil, nil)
	require.NoError(t, err)

	migrateJob := documentOfKind(t, doc, "Job")
	require.NotEmpty(t, migrateJob, "the migration job must be present")
	assert.Contains(t, migrateJob, "PYTHONPATH", "django-admin cannot import the settings module without it")
}

// documentOfKind returns the first document in a multi-document YAML string
// with the given kind.
func documentOfKind(t *testing.T, doc string, kind string) string {
	t.Helper()

	for _, chunk := range strings.Split(doc, "\n---\n") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		var parsed struct {
			Kind string `json:"kind"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(chunk), &parsed))
		if parsed.Kind == kind {
			return chunk
		}
	}

	return ""
}

// TestDjangoYaml_MigrationWaitsForDatabase covers the race where the migration
// job started alongside the database and failed its first attempt with
// connection refused. It completed only because the job retried, which leaves
// migrations one slow database start away from failing outright.
func TestDjangoYaml_MigrationWaitsForDatabase(t *testing.T) {
	doc, err := djangoYaml("myapp", "myorg/myapp:v1", "myapp.settings", 1, "dev", 20, true, nil, nil)
	require.NoError(t, err)

	migrateJob := documentOfKind(t, doc, "Job")
	require.NotEmpty(t, migrateJob)
	assert.Contains(t, migrateJob, "wait-for-database")
	assert.Contains(t, migrateJob, "pg_isready", "the wait has to test the database, not just sleep")
}
