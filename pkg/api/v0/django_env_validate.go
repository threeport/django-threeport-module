package v0

import (
	"fmt"
	"regexp"
	"strings"

	util "github.com/threeport/threeport/pkg/util/v0"
)

// envVarNameRegexp matches the names Kubernetes accepts for container env
// vars that Django apps use in practice.
var envVarNameRegexp = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnvVarNames are wired by the module itself and cannot be set or
// overridden through Env or SecretEnvVars. PYTHONPATH is set on the migration
// job only.
var reservedEnvVarNames = map[string]bool{
	"DATABASE_URL":           true,
	"SECRET_KEY":             true,
	"DJANGO_SETTINGS_MODULE": true,
	"PYTHONPATH":             true,
}

// ValidateDjangoEnvVars checks the Env and SecretEnvVars of one definition or
// instance: every literal is KEY=VALUE with a usable key, every secret
// reference is complete, no name is reserved, and no name appears more than
// once across the two fields. Only names and structure are inspected, never
// values, so it is safe to call on Env entries whose values are already
// encrypted.
//
// The API enforces this on create and update, and the config package calls it
// too so a bad config fails client-side with the same rules.
func ValidateDjangoEnvVars(env *[]string, secretEnvVars *[]DjangoSecretEnvVar) error {
	multiError := util.MultiError{}
	seen := map[string]bool{}

	check := func(name, where string) {
		switch {
		case !envVarNameRegexp.MatchString(name):
			multiError.AppendError(fmt.Errorf(
				"invalid value for %s: %q is not a valid environment variable name", where, name,
			))
		case reservedEnvVarNames[name]:
			multiError.AppendError(fmt.Errorf(
				"invalid value for %s: %s is set by the module and cannot be overridden", where, name,
			))
		case seen[name]:
			multiError.AppendError(fmt.Errorf(
				"invalid value for %s: %s is set more than once across Env and SecretEnvVars", where, name,
			))
		}
		seen[name] = true
	}

	if env != nil {
		for i, entry := range *env {
			name, _, found := strings.Cut(entry, "=")
			if !found {
				multiError.AppendError(fmt.Errorf(
					"invalid value for Env[%d]: entry is not in KEY=VALUE form", i,
				))
				continue
			}
			check(name, fmt.Sprintf("Env[%d]", i))
		}
	}

	if secretEnvVars != nil {
		for i, secretEnvVar := range *secretEnvVars {
			where := fmt.Sprintf("SecretEnvVars[%d]", i)
			for _, field := range []struct{ name, value string }{
				{"Name", secretEnvVar.Name},
				{"SecretName", secretEnvVar.SecretName},
				{"SecretKey", secretEnvVar.SecretKey},
			} {
				if field.value == "" {
					multiError.AppendError(fmt.Errorf("missing required field: %s.%s", where, field.name))
				}
			}
			if secretEnvVar.Name != "" {
				check(secretEnvVar.Name, where)
			}
		}
	}

	return multiError.Error()
}

// validateIncomingEnvVars is the hook-side wrapper: it reports a bad request,
// so the API answers 400 and the caller learns which entry to fix.
func validateIncomingEnvVars(env *[]string, secretEnvVars *[]DjangoSecretEnvVar) error {
	if err := ValidateDjangoEnvVars(env, secretEnvVars); err != nil {
		return util.NewBadRequestError(err.Error())
	}
	return nil
}
