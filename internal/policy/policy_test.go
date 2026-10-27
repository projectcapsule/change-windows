// Copyright 2026 Project Capsule Authors
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	api "github.com/projectcapsule/schedule-action-controller/api/v1alpha1"
	"github.com/projectcapsule/schedule-action-controller/internal/testutil"
)

func TestValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*api.ChangeWindow)
	}{
		{"empty selectors", func(a *api.ChangeWindow) { a.Spec.NamespaceSelectors = nil }},
		{"bad selector", func(a *api.ChangeWindow) {
			a.Spec.NamespaceSelectors = []metav1.LabelSelector{{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "x", Operator: "invalid"}}}}
		}},
		{"cluster target", func(a *api.ChangeWindow) {
			a.Spec.Targets[0].APIVersion = "v1"
			a.Spec.Targets[0].Kind = "Namespace"
		}},
		{"missing target", func(a *api.ChangeWindow) { a.Spec.Admission[0].Targets = []string{"missing"} }},
		{"invalid template", func(a *api.ChangeWindow) { a.Spec.Admission[0].Message = "{{" }},
		{"invalid warning", func(a *api.ChangeWindow) {
			a.Spec.Config.Admission.Warnings = []api.AdmissionWarning{{Name: "soon", When: "BeforeStart", Message: "soon", RequestMatch: a.Spec.Admission[0].RequestMatch}}
		}},
		{"missing executor", func(a *api.ChangeWindow) {
			a.Spec.ServiceAccount = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testutil.Action()
			tc.mutate(a)
			if _, err := Compile(a, testutil.Mapper()); err == nil {
				t.Fatal("accepted invalid policy")
			}
		})
	}
}

func TestServiceAccountValidation(t *testing.T) {
	for _, mode := range []string{"admission", "actions", "nodes"} {
		for _, tc := range []struct {
			name string
			sa   *api.ServiceAccountReference
		}{
			{"missing", nil},
			{"empty", &api.ServiceAccountReference{}},
			{"missing namespace", &api.ServiceAccountReference{Name: "executor"}},
			{"missing name", &api.ServiceAccountReference{Namespace: "dev"}},
			{"invalid namespace", &api.ServiceAccountReference{Namespace: "Not/a-namespace", Name: "executor"}},
			{"long namespace", &api.ServiceAccountReference{Namespace: strings.Repeat("a", 64), Name: "executor"}},
			{"invalid name", &api.ServiceAccountReference{Namespace: "dev", Name: "Not/an-account"}},
			{"long name", &api.ServiceAccountReference{Namespace: "dev", Name: strings.Repeat("a", 254)}},
			{"valid", &api.ServiceAccountReference{Namespace: "dev", Name: "window.executor"}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				window := testutil.Action()
				window.Spec.ServiceAccount = tc.sa
				switch mode {
				case "actions":
					window.Spec.Actions = []api.ResourceAction{{Name: "apply", Targets: []string{"deployments"}, OnStart: &api.ActionStep{Apply: api.Apply{Template: "spec: {}"}}}}
				case "nodes":
					window.Spec.NamespaceSelectors = nil
					window.Spec.Targets[0].APIVersion, window.Spec.Targets[0].Kind = "v1", "Node"
					window.Spec.Admission[0].Subresources = nil
				}
				_, err := Compile(window, testutil.Mapper())
				if tc.name == "valid" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "spec.serviceAccount") {
					t.Fatalf("expected service account rejection, got %v", err)
				}
			})
		}
	}
}

func TestStoreLifecycle(t *testing.T) {
	s := NewStore(testutil.Mapper())
	a := testutil.Action()
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	if err := s.Upsert(a); err != nil {
		t.Fatal(err)
	}
	firstSet, _ := s.Candidates(gvr)
	first := slices.Collect(firstSet.All())
	a.Spec.Admission[0].Message = "mutated"
	if first[0].Source.Spec.Admission[0].Message == "mutated" {
		t.Fatal("published source was not copied")
	}
	if err := s.Upsert(a); err != nil {
		t.Fatal(err)
	}
	secondSet, _ := s.Candidates(gvr)
	second := slices.Collect(secondSet.All())
	if first[0] != second[0] {
		t.Fatal("status/same-generation update recompiled")
	}
	a.Generation++
	if err := s.Upsert(a); err != nil {
		t.Fatal(err)
	}
	secondSet, _ = s.Candidates(gvr)
	second = slices.Collect(secondSet.All())
	if first[0] == second[0] {
		t.Fatal("spec change not invalidated")
	}
	a.Spec.Schedules[0].Cron = "bad"
	a.Generation++
	if err := s.Upsert(a); err == nil {
		t.Fatal("invalid update accepted")
	}
	if _, err := s.Candidates(gvr); err == nil {
		t.Fatal("invalid policy failed open")
	}
	s.Delete(a.Name)
	list, err := s.Candidates(gvr)
	if err != nil || !list.Empty() {
		t.Fatalf("delete did not prune: %v %v", list, err)
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := NewStore(testutil.Mapper())
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := testutil.Action()
			a.Name = fmt.Sprint(i)
			if i%2 == 0 {
				a.Spec.Targets[0].APIVersion, a.Spec.Targets[0].Kind = "*", "*"
			}
			for range 20 {
				if err := s.Upsert(a); err != nil {
					t.Error(err)
				}
				if candidates, err := s.Candidates(schema.GroupVersionResource{}); err != nil {
					t.Error(err)
				} else {
					for c := range candidates.All() {
						if c.Source == nil {
							t.Error("incomplete snapshot")
						}
					}
				}
			}
			s.Delete(a.Name)
		}()
	}
	wg.Wait()
}

func TestRenderLimits(t *testing.T) {
	tmpl, _ := Parse("{{ .missing }}")
	if _, err := Render(tmpl, map[string]any{}, 100); err == nil {
		t.Fatal("missing key accepted")
	}
	tmpl, _ = Parse("0123456789")
	if _, err := Render(tmpl, nil, 5); err == nil {
		t.Fatal("output limit ignored")
	}
}

func TestChangeWindowExamples(t *testing.T) {
	files, err := filepath.Glob("../../playground/manifests/examples/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples found: %v", err)
	}
	files = append(files, "../../playground/manifests/demo/windows/after-hours.yaml")
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			reader := kyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
			accounts := map[types.NamespacedName]bool{}
			var windows []*api.ChangeWindow
			for {
				document, err := reader.Read()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				var kind metav1.TypeMeta
				if err := yaml.Unmarshal(document, &kind); err != nil {
					t.Fatal(err)
				}
				switch kind.Kind {
				case "ChangeWindow":
					window := &api.ChangeWindow{}
					if err := yaml.UnmarshalStrict(document, window); err != nil {
						t.Fatal(err)
					}
					if _, err := Compile(window, testutil.Mapper()); err != nil {
						t.Fatalf("documented example fails policy validation: %v", err)
					}
					windows = append(windows, window)
				case "ServiceAccount":
					var account corev1.ServiceAccount
					if err := yaml.UnmarshalStrict(document, &account); err != nil {
						t.Fatal(err)
					}
					accounts[types.NamespacedName{Namespace: account.Namespace, Name: account.Name}] = true
				}
			}
			if len(windows) == 0 {
				t.Fatal("example contains no ChangeWindow")
			}
			for _, window := range windows {
				identity := window.Spec.ServiceAccount
				if !accounts[types.NamespacedName{Namespace: identity.Namespace, Name: identity.Name}] {
					t.Fatalf("example does not deploy the ServiceAccount referenced by %s: %s/%s", window.Name, identity.Namespace, identity.Name)
				}
			}
		})
	}
}
