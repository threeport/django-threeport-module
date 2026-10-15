package django

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kube "github.com/threeport/threeport/pkg/kube/v0"
	"sigs.k8s.io/yaml"

	v0 "django-threeport-module/pkg/api/v0"
)

type renderedEnv struct {
	Value     string
	SecretRef string
}

// envByContainer renders base plus overlay through Kustomize and returns each
// workload container's env as name -> value or "secretName/key", keyed by
// "<Kind>/<container>".
func envByContainer(t *testing.T, base string, overlay *string) map[string]map[string]renderedEnv {
	t.Helper()

	body := ""
	if overlay != nil {
		body = *overlay
	}
	docs, err := kube.RenderKustomizeOverlay(base, body)
	require.NoError(t, err)

	out := map[string]map[string]renderedEnv{}
	for _, doc := range docs {
		var parsed struct {
			Kind string `json:"kind"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name string `json:"name"`
							Env  []struct {
								Name      string `json:"name"`
								Value     string `json:"value"`
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
		require.NoError(t, json.Unmarshal(doc, &parsed))
		for _, c := range parsed.Spec.Template.Spec.Containers {
			env := map[string]renderedEnv{}
			for _, e := range c.Env {
				r := renderedEnv{Value: e.Value}
				if e.ValueFrom.SecretKeyRef.Name != "" {
					r.SecretRef = e.ValueFrom.SecretKeyRef.Name + "/" + e.ValueFrom.SecretKeyRef.Key
				}
				env[e.Name] = r
			}
			out[parsed.Kind+"/"+c.Name] = env
		}
	}
	return out
}

func TestDjangoYaml_CustomEnv(t *testing.T) {
	doc, err := djangoYaml(
		"myapp", "myorg/myapp:v1", "myapp.settings", 1, "dev", 20, true,
		[]string{"FEATURE_X=on", "URL=http://a?b=c"},
		[]v0.DjangoSecretEnvVar{{Name: "API_KEY", SecretName: "myapp-keys", SecretKey: "api"}},
	)
	require.NoError(t, err)

	got := envByContainer(t, doc, nil)
	for _, container := range []string{"Deployment/django", "Job/migrate"} {
		env := got[container]
		assert.Equal(t, "on", env["FEATURE_X"].Value, container)
		assert.Equal(t, "http://a?b=c", env["URL"].Value, "only the first = splits, %s", container)
		assert.Equal(t, "myapp-keys/api", env["API_KEY"].SecretRef, container)
		assert.Contains(t, env, "DATABASE_URL", "module wiring must remain, %s", container)
	}
}

func TestDjangoInstanceKustomizeOverlay_NilWithoutVars(t *testing.T) {
	overlay, err := djangoInstanceKustomizeOverlay("myapp", true, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, overlay)
}

// The instance wins for a name set on both sides, whichever of literal or
// secret reference each side used, and names on only one side survive.
func TestDjangoInstanceKustomizeOverlay_OverridesDefinition(t *testing.T) {
	base, err := djangoYaml(
		"myapp", "myorg/myapp:v1", "myapp.settings", 1, "dev", 20, true,
		[]string{"LITERAL_A=def", "LITERAL_B=def", "DEF_ONLY=def"},
		[]v0.DjangoSecretEnvVar{{Name: "SECRET_C", SecretName: "def-secret", SecretKey: "k"}},
	)
	require.NoError(t, err)

	overlay, err := djangoInstanceKustomizeOverlay(
		"myapp", true,
		[]string{"LITERAL_A=inst", "SECRET_C=inst-literal", "INST_ONLY=inst"},
		[]v0.DjangoSecretEnvVar{{Name: "LITERAL_B", SecretName: "inst-secret", SecretKey: "b"}},
	)
	require.NoError(t, err)
	require.NotNil(t, overlay)

	got := envByContainer(t, base, overlay)
	for _, container := range []string{"Deployment/django", "Job/migrate"} {
		env := got[container]
		assert.Equal(t, renderedEnv{Value: "inst"}, env["LITERAL_A"], container)
		assert.Equal(t, renderedEnv{SecretRef: "inst-secret/b"}, env["LITERAL_B"], "literal -> secret, %s", container)
		assert.Equal(t, renderedEnv{Value: "inst-literal"}, env["SECRET_C"], "secret -> literal, %s", container)
		assert.Equal(t, renderedEnv{Value: "def"}, env["DEF_ONLY"], container)
		assert.Equal(t, renderedEnv{Value: "inst"}, env["INST_ONLY"], container)
		assert.Contains(t, env, "DATABASE_URL", container)
	}

	// the database container is untouched
	assert.NotContains(t, got["Deployment/postgres"], "INST_ONLY")
}

func TestDjangoInstanceKustomizeOverlay_NoMigrationsNoJobPatch(t *testing.T) {
	base, err := djangoYaml("myapp", "myorg/myapp:v1", "", 1, "dev", 20, false, nil, nil)
	require.NoError(t, err)

	overlay, err := djangoInstanceKustomizeOverlay("myapp", false, []string{"A=1"}, nil)
	require.NoError(t, err)
	require.NotNil(t, overlay)
	assert.NotContains(t, *overlay, "myapp-migrate")

	got := envByContainer(t, base, overlay)
	assert.Equal(t, "1", got["Deployment/django"]["A"].Value)
}

func TestDjangoInstanceKustomizeOverlay_Deterministic(t *testing.T) {
	a, err := djangoInstanceKustomizeOverlay("myapp", true, []string{"B=1", "A=2", "C=3"}, nil)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		b, err := djangoInstanceKustomizeOverlay("myapp", true, []string{"B=1", "A=2", "C=3"}, nil)
		require.NoError(t, err)
		assert.Equal(t, *a, *b)
	}
}

// threeport's workload instance reconciler renders the overlay against the
// workload resource definitions' JSON, joined with "---", not against the YAML
// document the definition was created from. The overlay has to work on that
// exact shape.
func TestDjangoInstanceKustomizeOverlay_AppliesToJSONBase(t *testing.T) {
	yamlBase, err := djangoYaml(
		"myapp", "myorg/myapp:v1", "myapp.settings", 1, "dev", 20, true,
		[]string{"A=def"}, nil,
	)
	require.NoError(t, err)

	var jsonDocs []string
	for _, chunk := range strings.Split(yamlBase, "\n---\n") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		j, err := yaml.YAMLToJSON([]byte(chunk))
		require.NoError(t, err)
		jsonDocs = append(jsonDocs, string(j))
	}
	jsonBase := strings.Join(jsonDocs, "\n---\n")

	overlay, err := djangoInstanceKustomizeOverlay(
		"myapp", true, []string{"A=inst"},
		[]v0.DjangoSecretEnvVar{{Name: "B", SecretName: "s", SecretKey: "k"}},
	)
	require.NoError(t, err)

	got := envByContainer(t, jsonBase, overlay)
	for _, container := range []string{"Deployment/django", "Job/migrate"} {
		assert.Equal(t, renderedEnv{Value: "inst"}, got[container]["A"], container)
		assert.Equal(t, renderedEnv{SecretRef: "s/k"}, got[container]["B"], container)
	}
}
