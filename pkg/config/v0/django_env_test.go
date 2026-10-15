package v0

import (
	"strings"
	"testing"

	encryption "github.com/threeport/threeport/pkg/encryption/v0"
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

// storedEnv returns Env as the API stores it: values encrypted with key.
func storedEnv(t *testing.T, key string, entries ...string) *[]string {
	t.Helper()
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		k, v, _ := strings.Cut(entry, "=")
		enc, err := encryption.Encrypt(key, v)
		require.NoError(t, err)
		out = append(out, k+"="+enc)
	}
	return &out
}

func TestCheckEnvUnchanged(t *testing.T) {
	key, err := encryption.GenerateKey()
	require.NoError(t, err)

	existingEnv := storedEnv(t, key, "A=one", "B=two")
	existingSecrets := &[]api_v0.DjangoSecretEnvVar{{Name: "S", SecretName: "n", SecretKey: "k"}}
	same := []DjangoSecretEnvVarValues{{Name: "S", SecretName: "n", SecretKey: "k"}}
	check := func(env *[]string, secrets []DjangoSecretEnvVarValues) error {
		return checkEnvUnchanged("x", env, secrets, existingEnv, existingSecrets, key)
	}

	assert.NoError(t, check(&[]string{"B=two", "A=one"}, same), "same entries in a different order")
	assert.NoError(t, check(&[]string{"A=" + encryption.RedactedValuePlaceholder, "B=two"}, same),
		"a redacted placeholder, as get prints it, is the stored value")
	assert.NoError(t, checkEnvUnchanged("x", nil, nil, nil, nil, ""), "nothing set needs no key")

	assert.Error(t, check(&[]string{"A=changed", "B=two"}, same), "value-only edit")
	assert.Error(t, check(&[]string{"A=one"}, same), "entry removed")
	assert.Error(t, check(&[]string{"A=one", "B=two", "C=new"}, same), "entry added")
	assert.Error(t, check(&[]string{"A=one", "C=two"}, same), "name replaced")
	assert.Error(t, check(existingEnv, nil), "secret removed")
	assert.Error(t, check(&[]string{"A=one", "B=two"},
		[]DjangoSecretEnvVarValues{{Name: "S", SecretName: "n", SecretKey: "other"}}), "secret changed")

	err = check(&[]string{"A=changed", "B=two"}, same)
	assert.ErrorContains(t, err, "cannot be changed after creation")
}

func TestCheckEnvUnchanged_NeedsKeyWhenEnvIsInvolved(t *testing.T) {
	key, err := encryption.GenerateKey()
	require.NoError(t, err)
	stored := storedEnv(t, key, "A=one")

	assert.ErrorContains(t,
		checkEnvUnchanged("x", &[]string{"A=one"}, nil, stored, nil, ""),
		"encryption key", "a missing key must not read as unchanged")
	assert.ErrorContains(t,
		checkEnvUnchanged("x", nil, nil, stored, nil, ""),
		"encryption key", "removing Env still needs the key to see what was removed")
	assert.NoError(t, checkEnvUnchanged("x", nil, nil, nil, nil, ""))
}

func TestDecryptOrRedact(t *testing.T) {
	inst := api_v0.DjangoInstance{Env: &[]string{"TOKEN=abc"}}
	redacted, err := decryptOrRedactInstance(inst, "")
	require.NoError(t, err)
	require.NotNil(t, redacted.Env)
	assert.NotContains(t, (*redacted.Env)[0], "abc")
}
