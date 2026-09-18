package config

import (
	"reflect"
	"testing"
)

func TestResolveAudioNodesExplicitWins(t *testing.T) {
	resolved, from := ResolveAudioNodes([]string{"m4"}, []string{"m4", "pi"}, []string{"pi"}, []string{"m4"})
	if from != AudioNodeResolutionExplicit || !reflect.DeepEqual(resolved, []string{"m4"}) {
		t.Fatalf("resolved = %+v from %q, want [m4] from explicit", resolved, from)
	}
}

func TestResolveAudioNodesShowMinusExclude(t *testing.T) {
	resolved, from := ResolveAudioNodes(nil, []string{"m4", "pi", "pi3"}, []string{"pi"}, []string{"m4"})
	if from != AudioNodeResolutionShow || !reflect.DeepEqual(resolved, []string{"m4", "pi3"}) {
		t.Fatalf("resolved = %+v from %q, want [m4 pi3] from show", resolved, from)
	}
}

func TestResolveAudioNodesFallsBackToDefault(t *testing.T) {
	resolved, from := ResolveAudioNodes(nil, nil, nil, []string{"m4"})
	if from != AudioNodeResolutionDefault || !reflect.DeepEqual(resolved, []string{"m4"}) {
		t.Fatalf("resolved = %+v from %q, want [m4] from default", resolved, from)
	}
}

func TestResolveAudioNodesDefaultCanBeEmpty(t *testing.T) {
	resolved, from := ResolveAudioNodes(nil, nil, nil, nil)
	if from != AudioNodeResolutionDefault || len(resolved) != 0 {
		t.Fatalf("resolved = %+v from %q, want empty from default", resolved, from)
	}
}

func TestResolveAudioNodesIdenticalAcrossCallers(t *testing.T) {
	// The same three inputs must produce the same resolved list and tier
	// no matter which consumer calls it (ADR-049 decision 10's "identical
	// wherever it is asked for the same object" requirement).
	explicit := []string{}
	showAudioNodes := []string{"m4", "pi"}
	excludeNodes := []string{"pi"}
	defaultNodes := []string{"m4"}
	first, firstFrom := ResolveAudioNodes(explicit, showAudioNodes, excludeNodes, defaultNodes)
	second, secondFrom := ResolveAudioNodes(explicit, showAudioNodes, excludeNodes, defaultNodes)
	if firstFrom != secondFrom || !reflect.DeepEqual(first, second) {
		t.Fatalf("two calls with identical inputs diverged: (%+v, %q) vs (%+v, %q)", first, firstFrom, second, secondFrom)
	}
}
