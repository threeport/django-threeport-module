package django

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	v0 "django-threeport-module/pkg/api/v0"
)

// appContainerName and migrateContainerName are the container names
// djangoYaml() gives the application Deployment and the migration Job. The
// overlay patches target containers by these names.
const (
	appContainerName     = "django"
	migrateContainerName = "migrate"
)

// customEnvEntries converts decrypted KEY=VALUE entries and secret references
// into Kubernetes container env entries, sorted by name so the output is
// stable. Entries that are not in KEY=VALUE form are skipped: they are
// rejected by validation before they get here.
func customEnvEntries(
	env []string,
	secretEnvVars []v0.DjangoSecretEnvVar,
) []interface{} {
	return envEntries(env, secretEnvVars, false)
}

// envEntries builds the entries customEnvEntries describes. With forPatch set,
// each entry also nulls the field it does not use. A strategic merge patch
// merges field by field within a same-named list item, so without this a
// literal overriding a secret reference (or the reverse) would leave both
// value and valueFrom set, which Kubernetes rejects.
func envEntries(
	env []string,
	secretEnvVars []v0.DjangoSecretEnvVar,
	forPatch bool,
) []interface{} {
	entries := make(map[string]interface{}, len(env)+len(secretEnvVars))
	for _, kv := range env {
		name, value, found := strings.Cut(kv, "=")
		if !found || name == "" {
			continue
		}
		entry := map[string]interface{}{"name": name, "value": value}
		if forPatch {
			entry["valueFrom"] = nil
		}
		entries[name] = entry
	}
	for _, secretEnvVar := range secretEnvVars {
		entry := map[string]interface{}{
			"name": secretEnvVar.Name,
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": secretEnvVar.SecretName,
					"key":  secretEnvVar.SecretKey,
				},
			},
		}
		if forPatch {
			entry["value"] = nil
		}
		entries[secretEnvVar.Name] = entry
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	sorted := make([]interface{}, 0, len(names))
	for _, name := range names {
		sorted = append(sorted, entries[name])
	}

	return sorted
}

// djangoInstanceKustomizeOverlay renders an instance's env vars as the body of
// a kustomization.yaml: one strategic merge patch for the application
// Deployment and, when migrations run, one for the migration Job. Core EnvVar
// lists merge on name, so a variable named on both the definition and the
// instance takes the instance's value, whichever of literal or secret
// reference either side used. It returns nil when the instance has no vars.
//
// env is the instance's decrypted KEY=VALUE entries: the caller decrypts, this
// function never sees ciphertext.
func djangoInstanceKustomizeOverlay(
	definitionName string,
	runMigrations bool,
	env []string,
	secretEnvVars []v0.DjangoSecretEnvVar,
) (*string, error) {
	entries := envEntries(env, secretEnvVars, true)
	if len(entries) == 0 {
		return nil, nil
	}

	patch := func(apiVersion, kind, name, container string) (map[string]interface{}, error) {
		body, err := yaml.Marshal(map[string]interface{}{
			"apiVersion": apiVersion,
			"kind":       kind,
			"metadata":   map[string]interface{}{"name": name},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name": container,
								"env":  entries,
							},
						},
					},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal %s patch: %w", kind, err)
		}
		return map[string]interface{}{"patch": string(body)}, nil
	}

	deploymentPatch, err := patch("apps/v1", "Deployment", definitionName, appContainerName)
	if err != nil {
		return nil, err
	}
	patches := []interface{}{deploymentPatch}

	if runMigrations {
		jobPatch, err := patch("batch/v1", "Job", fmt.Sprintf("%s-migrate", definitionName), migrateContainerName)
		if err != nil {
			return nil, err
		}
		patches = append(patches, jobPatch)
	}

	out, err := yaml.Marshal(map[string]interface{}{"patches": patches})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal kustomize overlay: %w", err)
	}
	overlay := string(out)

	return &overlay, nil
}
