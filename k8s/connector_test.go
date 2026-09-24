package k8s

import (
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestValidateGroup(t *testing.T) {
	t.Parallel()

	suite := []struct {
		name    string
		group   string
		wantErr string
	}{
		{
			name:  "apigroup",
			group: "apps.k8s.io",
		},
		{
			name:  "two labels",
			group: "cert-manager.io",
		},
		{
			name:  "many labels",
			group: "snapshot.storage.k8s.io",
		},
		{
			name:  "empty is the core group",
			group: "",
		},
		{
			name:  "dotless legacy group",
			group: "apps",
		},
		{
			name:    "uppercase",
			group:   "Apps.k8s.io",
			wantErr: "invalid format",
		},
		{
			name:    "slash",
			group:   "apps.k8s.io/v1",
			wantErr: "invalid format",
		},
		{
			name:    "leading space",
			group:   " apps.k8s.io",
			wantErr: "invalid format",
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateGroup(test.group)

			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				require.ErrorContains(t, err, test.group, "the error mentions the offending group")
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestValidateKind(t *testing.T) {
	t.Parallel()

	suite := []struct {
		name    string
		kind    string
		wantErr string
	}{
		{
			name: "kinds are camelcase",
			kind: "Deployment",
		},
		{
			name: "several words",
			kind: "VirtualMachineInstance",
		},
		{
			name: "digits",
			kind: "PodDisruptionBudgetV2",
		},
		{
			name:    "dot",
			kind:    "apps.Deployment",
			wantErr: "invalid format",
		},
		{
			name:    "slash",
			kind:    "apps/Deployment",
			wantErr: "invalid format",
		},
		{
			name:    "space",
			kind:    "Virtual Machine",
			wantErr: "invalid format",
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateKind(test.kind)

			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				require.ErrorContains(t, err, test.kind, "the error mentions the offending kind")
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestParseGroupKind(t *testing.T) {
	t.Parallel()

	suite := []struct {
		name    string
		str     string
		want    schema.GroupKind
		wantErr string
	}{
		{
			name: "group and kind",
			str:  "apps.k8s.io/Deployment",
			want: schema.GroupKind{Group: "apps.k8s.io", Kind: "Deployment"},
		},
		{
			name:    "no separator",
			str:     "Deployment",
			wantErr: `invalid format for group/kind "Deployment"`,
		},
		{
			name:    "empty",
			str:     "",
			wantErr: `invalid format for group/kind ""`,
		},
		{
			name:    "too many separators",
			str:     "apps.k8s.io/v1/Deployment",
			wantErr: `invalid format for kind "v1/Deployment"`,
		},
		{
			name: "core group",
			str:  "/ConfigMap",
			want: schema.GroupKind{Group: "", Kind: "ConfigMap"},
		},
		{
			name: "dotless legacy group",
			str:  "batch/CronJob",
			want: schema.GroupKind{Group: "batch", Kind: "CronJob"},
		},
		{
			name:    "bad group",
			str:     "Apps/Deployment",
			wantErr: `invalid format for group "Apps"`,
		},
		{
			name:    "bad kind",
			str:     "apps.k8s.io/Deployment!",
			wantErr: `invalid format for kind "Deployment!"`,
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseGroupKind(test.str)

			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				require.Zero(t, got, "a rejected group/kind is never returned")
				return
			}

			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestParseIgnoreResources(t *testing.T) {
	t.Parallel()

	suite := []struct {
		name    string
		str     string
		want    map[schema.GroupKind]struct{}
		wantErr string
	}{
		{
			name: "empty",
			str:  "",
			want: map[schema.GroupKind]struct{}{},
		},
		{
			name: "only separators",
			str:  ";;",
			want: map[schema.GroupKind]struct{}{},
		},
		{
			name: "single",
			str:  "apps.k8s.io/Deployment",
			want: map[schema.GroupKind]struct{}{
				{Group: "apps.k8s.io", Kind: "Deployment"}: {},
			},
		},
		{
			name: "several",
			str:  "apps.k8s.io/Deployment;snapshot.storage.k8s.io/VolumeSnapshot",
			want: map[schema.GroupKind]struct{}{
				{Group: "apps.k8s.io", Kind: "Deployment"}:                 {},
				{Group: "snapshot.storage.k8s.io", Kind: "VolumeSnapshot"}: {},
			},
		},
		{
			name: "empty entries are skipped",
			str:  ";apps/Deployment;;cert-manager.io/Certificate;/ConfigMap;",
			want: map[schema.GroupKind]struct{}{
				{Group: "apps", Kind: "Deployment"}:             {},
				{Group: "cert-manager.io", Kind: "Certificate"}: {},
				{Group: "", Kind: "ConfigMap"}:                  {},
			},
		},
		{
			name: "duplicates are folded",
			str:  "apps.k8s.io/Deployment;cert-manager.io/Certificate;apps.k8s.io/Deployment",
			want: map[schema.GroupKind]struct{}{
				{Group: "apps.k8s.io", Kind: "Deployment"}:      {},
				{Group: "cert-manager.io", Kind: "Certificate"}: {},
			},
		},
		{
			name:    "one bad entry rejects the whole list",
			str:     "apps.k8s.io/Deployment;nope",
			wantErr: `invalid format for group/kind "nope"`,
		},
		{
			name:    "entries are not trimmed",
			str:     "apps.k8s.io/Deployment; cert-manager.io/Certificate",
			wantErr: `invalid format for group " cert-manager.io"`,
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseIgnoreResources(test.str)

			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				require.Nil(t, got, "a rejected list is never partially returned")
				return
			}

			require.NoError(t, err)

			// filters are funcs, so compare the kinds they cover
			gks := make(map[schema.GroupKind]struct{}, len(got))
			for gk, filter := range got {
				require.NotNil(t, filter, "%s/%s has no filter", gk.Group, gk.Kind)
				gks[gk] = struct{}{}
			}
			require.Equal(t, test.want, gks)
		})
	}
}

func TestNewSkipRootPermsAndTime(t *testing.T) {
	t.Parallel()
	// inline so that the test never reads the developer's kubeconfig
	const kubeconf = `
apiVersion: v1
kind: Config
clusters:
- cluster: {server: https://cluster.example}
  name: test
contexts:
- context: {cluster: test, user: test}
  name: test
current-context: test
users:
- name: test
  user: {token: t}
`

	newK8s := func(t *testing.T, options ...Options) *k8s {
		t.Helper()
		k, err := New(t.Context(), &connectors.Options{}, "k8s+pvc", map[string]string{
			"location":   "k8s+pvc://cluster.example/ns/data",
			"kubeconfig": kubeconf,
		}, true, options...)
		require.NoError(t, err)
		return k
	}

	t.Run("off by default", func(t *testing.T) {
		require.False(t, newK8s(t).skipRootPermsAndTime)
	})

	t.Run("set by the option", func(t *testing.T) {
		require.True(t, newK8s(t, WithSkipRootPermsAndTime(true)).skipRootPermsAndTime)
	})

	t.Run("the option can turn it back off", func(t *testing.T) {
		require.False(t, newK8s(t, WithSkipRootPermsAndTime(false)).skipRootPermsAndTime)
	})
}
