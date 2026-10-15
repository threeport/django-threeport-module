package v0

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	util "github.com/threeport/threeport/pkg/util/v0"
	gorm "gorm.io/gorm"
)

func ptr[T any](v T) *T { return &v }

var envVarCases = []struct {
	name    string
	env     *[]string
	secrets *[]DjangoSecretEnvVar
	wantErr string
}{
	{name: "nothing set"},
	{name: "valid mix", env: &[]string{"A=1", "B="}, secrets: &[]DjangoSecretEnvVar{{Name: "C", SecretName: "s", SecretKey: "k"}}},
	{name: "value may contain =", env: &[]string{"A=b=c"}},
	{name: "entry without =", env: &[]string{"A"}, wantErr: "KEY=VALUE"},
	{name: "bad name", env: &[]string{"1A=x"}, wantErr: "not a valid environment variable name"},
	{name: "empty name", env: &[]string{"=x"}, wantErr: "not a valid environment variable name"},
	{name: "reserved DATABASE_URL", env: &[]string{"DATABASE_URL=x"}, wantErr: "set by the module"},
	{name: "reserved SECRET_KEY", env: &[]string{"SECRET_KEY=x"}, wantErr: "set by the module"},
	{name: "reserved DJANGO_SETTINGS_MODULE", env: &[]string{"DJANGO_SETTINGS_MODULE=x"}, wantErr: "set by the module"},
	{name: "reserved via secret ref", secrets: &[]DjangoSecretEnvVar{{Name: "SECRET_KEY", SecretName: "s", SecretKey: "k"}}, wantErr: "set by the module"},
	{name: "secret missing key", secrets: &[]DjangoSecretEnvVar{{Name: "A", SecretName: "s"}}, wantErr: "SecretKey"},
	{name: "secret missing name", secrets: &[]DjangoSecretEnvVar{{SecretName: "s", SecretKey: "k"}}, wantErr: "SecretEnvVars[0].Name"},
	{name: "duplicate across fields", env: &[]string{"A=1"}, secrets: &[]DjangoSecretEnvVar{{Name: "A", SecretName: "s", SecretKey: "k"}}, wantErr: "more than once"},
	{name: "duplicate in env", env: &[]string{"A=1", "A=2"}, wantErr: "more than once"},
}

func TestValidateDjangoEnvVars(t *testing.T) {
	for _, tt := range envVarCases {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDjangoEnvVars(tt.env, tt.secrets)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// The checks have to hold at the API, not only in the config package: a caller
// posting to the API directly never goes through the config layer.
func TestDjangoHooksRejectBadEnv(t *testing.T) {
	updateTx := func(dest interface{}) *gorm.DB {
		return &gorm.DB{Statement: &gorm.Statement{Dest: dest}}
	}

	for _, tt := range envVarCases {
		t.Run(tt.name, func(t *testing.T) {
			def := &DjangoDefinition{Env: tt.env, SecretEnvVars: tt.secrets}
			inst := &DjangoInstance{Env: tt.env, SecretEnvVars: tt.secrets}

			errs := map[string]error{
				"definition create": def.beforeCreate(nil),
				"definition update": (&DjangoDefinition{}).beforeUpdate(updateTx(def)),
				"instance create":   inst.beforeCreate(nil),
				"instance update":   (&DjangoInstance{}).beforeUpdate(updateTx(inst)),
			}
			for hook, err := range errs {
				if tt.wantErr == "" {
					assert.NoError(t, err, hook)
					continue
				}
				require.ErrorContains(t, err, tt.wantErr, hook)
				var httpErr *util.HttpError
				require.ErrorAs(t, err, &httpErr, hook)
				assert.Equal(t, http.StatusBadRequest, httpErr.StatusCode, hook)
			}
		})
	}
}

// An update that sends no env, such as the reconciler recording a foreign key,
// must not trip over the stored row's env.
func TestDjangoHooksIgnoreUnsentEnvOnUpdate(t *testing.T) {
	tx := &gorm.DB{Statement: &gorm.Statement{Dest: &DjangoInstance{KubernetesWorkloadInstanceID: ptr(uint(1))}}}
	loaded := &DjangoInstance{Env: &[]string{"bad"}}
	assert.NoError(t, loaded.beforeUpdate(tx), "only the incoming values are validated")
}
