package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCompileStrictRegex(t *testing.T) {
	cases := []struct {
		pattern string
		match   []string
		noMatch []string
	}{
		{"team-a", []string{"team-a"}, []string{"team-a-evil", "evil-team-a", "team-ab"}},
		{" team-a ", []string{"team-a"}, []string{"team-a-evil"}},
		{"team-a|team-b", []string{"team-a", "team-b"}, []string{"team-a-evil", "evil-team-b", "team-c"}},
		{"^team-a|team-b$", []string{"team-a", "team-b"}, []string{"team-a-evil", "evil-team-b"}},
		{"^team-.*$", []string{"team-a", "team-"}, []string{"x-team-a"}},
		{"app-ns-[0-9]*", []string{"app-ns-1", "app-ns-"}, []string{"app-ns-1x", "my-app-ns-1"}},
		{"(?i)TEAM-A", []string{"team-a"}, []string{"team-a-x"}},
	}

	for _, c := range cases {
		re, err := CompileStrictRegex(c.pattern)
		require.NoError(t, err, c.pattern)
		for _, m := range c.match {
			require.True(t, re.MatchString(m), "%q should match %q", c.pattern, m)
		}
		for _, m := range c.noMatch {
			require.False(t, re.MatchString(m), "%q should not match %q", c.pattern, m)
		}
	}
}

func TestCompileStrictRegexRejectsGroupBreakout(t *testing.T) {
	for _, pattern := range []string{"team-a)|(.*", ")|(", `\Qteam-a`, "[a-"} {
		_, err := CompileStrictRegex(pattern)
		require.Error(t, err, pattern)
	}
}

func TestParseNamespacePatternList(t *testing.T) {
	patterns, err := ParseNamespacePatternList("team-a, team-b ,,app-[0-9]+")
	require.NoError(t, err)
	require.Len(t, patterns, 3)
	require.True(t, MatchesAnyPattern(patterns, "team-b"))
	require.True(t, MatchesAnyPattern(patterns, "app-12"))
	require.False(t, MatchesAnyPattern(patterns, "team-a-x"))

	patterns, err = ParseNamespacePatternList("team-a,[invalid,team-b")
	require.Error(t, err)
	require.Len(t, patterns, 2)

	patterns, err = ParseNamespacePatternList("")
	require.NoError(t, err)
	require.Empty(t, patterns)
}

func TestParseNamespacePatternListLimits(t *testing.T) {
	// too long
	_, err := ParseNamespacePatternList(strings.Repeat("a", MaxNamespacePatternListLength+1))
	require.Error(t, err)

	// too many patterns
	_, err = ParseNamespacePatternList(strings.Repeat("a,", MaxNamespacePatterns+1))
	require.Error(t, err)

	// too expensive to compile
	patterns, err := ParseNamespacePatternList(`[\pL\pN]{999}`)
	require.Error(t, err)
	require.Empty(t, patterns)
	patterns, err = ParseNamespacePatternList(strings.Repeat(`[a-z]{999},`, 40))
	require.Error(t, err)
	require.Empty(t, patterns)

	// StringToPatternList yields no patterns at all for lists over the limits
	require.Empty(t, StringToPatternList(strings.Repeat("a,", MaxNamespacePatterns+1)))

	// regular lists are well below the limits
	names := make([]string, 0, MaxNamespacePatterns)
	for i := 0; i < MaxNamespacePatterns; i++ {
		names = append(names, "namespace-with-a-long-name-"+strings.Repeat("x", i%20))
	}
	_, err = ParseNamespacePatternList(strings.Join(names, ","))
	require.NoError(t, err)
	_, err = ParseNamespacePatternList(`[a-z0-9]([-a-z0-9]{1}[a-z0-9]{2})?,kube-.*,team-(a|b|c)`)
	require.NoError(t, err)
}

func TestNamespaceFilter(t *testing.T) {
	filter, err := NewNamespaceFilter([]string{"kube-system", " platform-.*", ""})
	require.NoError(t, err)
	require.True(t, filter.ShouldExclude("kube-system"))
	require.True(t, filter.ShouldExclude("platform-x"))
	require.False(t, filter.ShouldExclude("team-a"))

	_, err = NewNamespaceFilter([]string{"kube-system", "platform-[a-"})
	require.Error(t, err)

	var nilFilter *NamespaceFilter
	require.False(t, nilFilter.ShouldExclude("kube-system"))
}

func TestPushPolicy(t *testing.T) {
	policy, err := NewPushPolicy([]string{"platform", " cert-.*", ""})
	require.NoError(t, err)
	require.True(t, policy.AllowsSourceNamespace("platform"))
	require.True(t, policy.AllowsSourceNamespace("cert-manager"))
	require.False(t, policy.AllowsSourceNamespace("platform-evil"))
	require.False(t, policy.AllowsSourceNamespace("team-a"))

	_, err = NewPushPolicy([]string{"platform)|(.*"})
	require.Error(t, err)

	empty, err := NewPushPolicy([]string{""})
	require.NoError(t, err)
	require.False(t, empty.AllowsSourceNamespace("platform"))

	var nilPolicy *PushPolicy
	require.False(t, nilPolicy.AllowsSourceNamespace("platform"))
}

func TestIsPushPermitted(t *testing.T) {
	policy, err := NewPushPolicy([]string{"platform"})
	require.NoError(t, err)
	r := &GenericReplicator{ReplicatorConfig: ReplicatorConfig{PushPolicy: policy}}

	plain := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}}
	consenting := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "team-c",
		Annotations: map[string]string{AllowPushFrom: "team-a|team-x"},
	}}

	require.NoError(t, r.IsPushPermitted("platform", plain))
	require.Error(t, r.IsPushPermitted("team-a", plain))
	require.NoError(t, r.IsPushPermitted("team-a", consenting))
	require.Error(t, r.IsPushPermitted("team-a-evil", consenting))

	unconfigured := &GenericReplicator{}
	require.Error(t, unconfigured.IsPushPermitted("platform", plain))
	require.NoError(t, unconfigured.IsPushPermitted("team-x", consenting))

	// RBAC objects may only be pushed from trusted namespaces
	rbac := &GenericReplicator{ReplicatorConfig: ReplicatorConfig{PushPolicy: policy, PushRequiresTrustedSource: true}}
	require.NoError(t, rbac.IsPushPermitted("platform", plain))
	require.NoError(t, rbac.IsPushPermitted("platform", consenting))
	require.Error(t, rbac.IsPushPermitted("team-a", consenting))
}

func TestCheckPushTarget(t *testing.T) {
	target := func(annotations map[string]string) metav1.Object {
		return &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "team-b", Annotations: annotations}}
	}

	// objects that were not created by the replicator are never overwritten
	require.Error(t, CheckPushTarget("team-a/x", target(nil)))
	require.Error(t, CheckPushTarget("team-a/x", target(map[string]string{"foo": "bar"})))

	// replicas of the same source are updated, replicas of other sources are not
	require.NoError(t, CheckPushTarget("team-a/x", target(map[string]string{ReplicatedFromSourceAnnotation: "team-a/x"})))
	require.Error(t, CheckPushTarget("team-a/x", target(map[string]string{ReplicatedFromSourceAnnotation: "team-c/x"})))

	// replicas created before the source was recorded are taken over
	require.NoError(t, CheckPushTarget("team-a/x", target(map[string]string{ReplicatedFromVersionAnnotation: "1"})))

	// pull targets and push sources are never overwritten
	require.Error(t, CheckPushTarget("team-a/x", target(map[string]string{
		ReplicatedFromVersionAnnotation: "1", ReplicateFromAnnotation: "team-c/x"})))
	require.Error(t, CheckPushTarget("team-a/x", target(map[string]string{
		ReplicatedFromSourceAnnotation: "team-a/x", ReplicateTo: "team-a"})))
	require.Error(t, CheckPushTarget("team-a/x", target(map[string]string{
		ReplicatedFromSourceAnnotation: "team-a/x", ReplicateToMatching: "foo=bar"})))
}

func TestIsPushReplicaOf(t *testing.T) {
	obj := &v1.Secret{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ReplicatedFromSourceAnnotation: "team-a/x"}}}
	require.True(t, IsPushReplicaOf(obj, "team-a/x"))
	require.False(t, IsPushReplicaOf(obj, "team-a/y"))
	require.False(t, IsPushReplicaOf(&v1.Secret{}, "team-a/x"))
}
