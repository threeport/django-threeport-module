package v0

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tpconfig_v0 "github.com/threeport/threeport/pkg/config/v0"
	util "github.com/threeport/threeport/pkg/util/v0"
)

// sentWorkloadInstanceId records the workload instance the last replace carried,
// or 0 when it carried none.
var sentWorkloadInstanceId uint

// replaceAPI serves the calls Replace makes. The django instance it holds is on
// runtime 7, the control plane's default runtime is 5, and 9 is a third cluster
// the user could name. It records the runtime ID the replace ends up sending.
func replaceAPI(t *testing.T, sent *uint) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		name := r.URL.Query().Get("name")

		switch {
		case r.Method == http.MethodPut && strings.Contains(path, "django-instances"):
			body := make([]byte, r.ContentLength)
			r.Body.Read(body)
			// the replaced object carries the runtime the caller decided on.
			// CreatedAt is present because the config renders an age from it.
			if index := strings.Index(string(body), `"KubernetesRuntimeInstanceID":`); index >= 0 {
				var id uint
				fmt.Sscanf(string(body)[index+len(`"KubernetesRuntimeInstanceID":`):], "%d", &id)
				*sent = id
			}
			sentWorkloadInstanceId = 0
			if index := strings.Index(string(body), `"KubernetesWorkloadInstanceID":`); index >= 0 {
				fmt.Sscanf(string(body)[index+len(`"KubernetesWorkloadInstanceID":`):], "%d", &sentWorkloadInstanceId)
			}
			fmt.Fprint(w, `{"Data":[{"ID":1,"Name":"myapp","CreatedAt":"2026-09-01T00:00:00Z"}]}`)

		case strings.Contains(path, "django-instances"):
			fmt.Fprint(w, `{"Data":[{"ID":1,"Name":"myapp","KubernetesRuntimeInstanceID":7,"DjangoDefinitionID":3,"KubernetesWorkloadInstanceID":42}]}`)

		case strings.Contains(path, "django-definitions"):
			fmt.Fprint(w, `{"Data":[{"ID":3,"Name":"myapp"}]}`)

		case strings.Contains(path, "kubernetes-runtime-instances"):
			// the default runtime is deliberately not the one the instance is
			// on: resolving the default instead of keeping the current runtime
			// has to be visible as a different ID, which is the whole defect
			switch {
			case r.URL.Query().Get("defaultruntime") == "true":
				fmt.Fprint(w, `{"Data":[{"ID":5,"Name":"default-cluster","DefaultRuntime":true}]}`)
			case name == "other-cluster" || strings.HasSuffix(path, "/9"):
				fmt.Fprint(w, `{"Data":[{"ID":9,"Name":"other-cluster"}]}`)
			default:
				fmt.Fprint(w, `{"Data":[{"ID":7,"Name":"this-cluster"}]}`)
			}

		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"Error":{"Message":"object not found"}}`)
		}
	}))
}

// TestDjangoInstanceConfig_Replace_KeepsTheCurrentRuntime is the point of the
// replace-specific resolver. The samples document omitting the runtime, so a
// config edited to change SubDomain carries no runtime at all - and resolving
// the default there would move the workload to another cluster on what reads as
// an unrelated edit.
func TestDjangoInstanceConfig_Replace_KeepsTheCurrentRuntime(t *testing.T) {
	var sent uint
	server := replaceAPI(t, &sent)
	defer server.Close()

	config := DjangoInstanceConfig{DjangoInstance: DjangoInstanceValues{
		Name:             util.Ptr("myapp"),
		SubDomain:        util.Ptr("www"),
		DjangoDefinition: &DjangoDefinitionValues{Name: util.Ptr("myapp")},
	}}

	_, err := config.Replace(server.Client(), apiAddr(server), "myapp", "")
	require.NoError(t, err)
	assert.Equal(t, uint(7), sent, "the instance has to stay on the runtime it is already on")
}

// TestDjangoInstanceConfig_Replace_RefusesToMove covers naming a different
// runtime. Moving a deployed workload is not something an in-place replace can
// do, so it is reported rather than half-performed.
func TestDjangoInstanceConfig_Replace_RefusesToMove(t *testing.T) {
	var sent uint
	server := replaceAPI(t, &sent)
	defer server.Close()

	config := DjangoInstanceConfig{DjangoInstance: DjangoInstanceValues{
		Name:                      util.Ptr("myapp"),
		KubernetesRuntimeInstance: &tpconfig_v0.KubernetesRuntimeInstanceValues{Name: util.Ptr("other-cluster")},
		DjangoDefinition:          &DjangoDefinitionValues{Name: util.Ptr("myapp")},
	}}

	_, err := config.Replace(server.Client(), apiAddr(server), "myapp", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "may not be moved")
	assert.Zero(t, sent, "nothing should have been written")
}

// TestDjangoInstanceConfig_Replace_AcceptsTheSameRuntimeNamed covers naming the
// runtime the instance is already on, which is not a move.
func TestDjangoInstanceConfig_Replace_AcceptsTheSameRuntimeNamed(t *testing.T) {
	var sent uint
	server := replaceAPI(t, &sent)
	defer server.Close()

	config := DjangoInstanceConfig{DjangoInstance: DjangoInstanceValues{
		Name:                      util.Ptr("myapp"),
		KubernetesRuntimeInstance: &tpconfig_v0.KubernetesRuntimeInstanceValues{Name: util.Ptr("this-cluster")},
		DjangoDefinition:          &DjangoDefinitionValues{Name: util.Ptr("myapp")},
	}}

	_, err := config.Replace(server.Client(), apiAddr(server), "myapp", "")
	require.NoError(t, err)
	assert.Equal(t, uint(7), sent)
}

// TestDjangoInstanceConfig_Replace_CarriesTheWorkloadInstance covers the owned
// relationship the reconciler sets. A replacement is a full PUT, so leaving the
// workload instance out asks the API to clear it - and an owned relationship is
// immutable once set, so the API rejects the whole replace. Every replace of a
// reconciled instance failed this way.
func TestDjangoInstanceConfig_Replace_CarriesTheWorkloadInstance(t *testing.T) {
	var sent uint
	server := replaceAPI(t, &sent)
	defer server.Close()

	config := DjangoInstanceConfig{DjangoInstance: DjangoInstanceValues{
		Name:             util.Ptr("myapp"),
		SubDomain:        util.Ptr("www"),
		DjangoDefinition: &DjangoDefinitionValues{Name: util.Ptr("myapp")},
	}}

	_, err := config.Replace(server.Client(), apiAddr(server), "myapp", "")
	require.NoError(t, err)
	assert.Equal(
		t, uint(42), sentWorkloadInstanceId,
		"the replacement has to carry the workload instance the reconciler set, or the API refuses it",
	)
}
