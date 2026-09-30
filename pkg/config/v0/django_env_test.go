package v0

import (
	"testing"

	util "github.com/threeport/threeport/pkg/util/v0"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api_v0 "django-threeport-module/pkg/api/v0"
)

func TestValidateEnvVars(t *testing.T) {
	secret := func(n, s, k string) DjangoSecretEnvVarValues {
		return DjangoSecretEnvVarValues{Name: n, SecretName: s, SecretKey: k}
	}
	tests := []struct {
		name    string
		env     *[]string
		secrets []DjangoSecretEnvVarValues
		wantErr string
	}{
		{name: "nothing set"},
		{name: "valid mix", env: &[]string{"A=1", "B="}, secrets: []DjangoSecretEnvVarValues{secret("C", "s", "k")}},
		{name: "value may contain =", env: &[]string{"A=b=c"}},
		{name: "entry without =", env: &[]string{"A"}, wantErr: "KEY=VALUE"},
		{name: "bad name", env: &[]string{"1A=x"}, wantErr: "not a valid environment variable name"},
		{name: "reserved literal", env: &[]string{"SECRET_KEY=x"}, wantErr: "set by the module"},
		{name: "reserved secret", secrets: []DjangoSecretEnvVarValues{secret("DATABASE_URL", "s", "k")}, wantErr: "set by the module"},
		{name: "secret missing key", secrets: []DjangoSecretEnvVarValues{secret("A", "s", "")}, wantErr: "SecretKey"},
		{name: "secret missing name", secrets: []DjangoSecretEnvVarValues{secret("", "s", "k")}, wantErr: "Name"},
		{name: "duplicate across fields", env: &[]string{"A=1"}, secrets: []DjangoSecretEnvVarValues{secret("A", "s", "k")}, wantErr: "more than once"},
		{name: "duplicate in env", env: &[]string{"A=1", "A=2"}, wantErr: "more than once"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEnvVars(tt.env, tt.secrets)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateIncludesEnvVars(t *testing.T) {
	def := DjangoDefinitionConfig{DjangoDefinition: DjangoDefinitionValues{
		Name: util.Ptr("app"), Image: util.Ptr("img"), Env: &[]string{"bad"},
	}}
	assert.ErrorContains(t, def.Validate(), "Env[0]")

	inst := DjangoInstanceConfig{DjangoInstance: DjangoInstanceValues{
		Name:             util.Ptr("app"),
		DjangoDefinition: &DjangoDefinitionValues{Name: util.Ptr("app")},
		SecretEnvVars:    []DjangoSecretEnvVarValues{{Name: "A"}},
	}}
	assert.ErrorContains(t, inst.Validate(), "SecretEnvVars[0]")
}

func TestCheckEnvUnchanged(t *testing.T) {
	existingEnv := &[]string{"A=cipher", "B=cipher"}
	existingSecrets := &[]api_v0.DjangoSecretEnvVar{{Name: "S", SecretName: "n", SecretKey: "k"}}
	same := []DjangoSecretEnvVarValues{{Name: "S", SecretName: "n", SecretKey: "k"}}

	assert.NoError(t, checkEnvUnchanged("x", &[]string{"B=new", "A=new"}, same, existingEnv, existingSecrets))
	assert.NoError(t, checkEnvUnchanged("x", nil, nil, nil, nil))
	assert.Error(t, checkEnvUnchanged("x", &[]string{"A=1"}, same, existingEnv, existingSecrets), "key removed")
	assert.Error(t, checkEnvUnchanged("x", existingEnv, nil, existingEnv, existingSecrets), "secret removed")
	assert.Error(t, checkEnvUnchanged("x", existingEnv,
		[]DjangoSecretEnvVarValues{{Name: "S", SecretName: "n", SecretKey: "other"}}, existingEnv, existingSecrets), "secret changed")
}

func TestDecryptOrRedact(t *testing.T) {
	inst := api_v0.DjangoInstance{Env: &[]string{"TOKEN=abc"}}
	redacted, err := decryptOrRedactInstance(inst, "")
	require.NoError(t, err)
	require.NotNil(t, redacted.Env)
	assert.NotContains(t, (*redacted.Env)[0], "abc")
}
