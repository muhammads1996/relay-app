package store

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestCompareAndSwapEnvironmentConcurrentCreation(t *testing.T) {
	s := open(t)
	const attempts = 8
	start := make(chan struct{})
	type outcome struct {
		env     Environment
		changed bool
		err     error
	}
	results := make(chan outcome, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			env := Environment{Name: "sit", Vars: map[string]string{"source": fmt.Sprintf("import-%d", i)}}
			<-start
			changed, err := s.CompareAndSwapEnvironment(&env, nil)
			results <- outcome{env: env, changed: changed, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner *Environment
	winners := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.changed {
			winners++
			env := result.env
			winner = &env
		}
	}
	if winners != 1 {
		t.Fatalf("successful concurrent creations = %d; want 1", winners)
	}
	stored, err := s.Environment("sit")
	if err != nil {
		t.Fatal(err)
	}
	if winner.ID == 0 || winner.ID != stored.ID || !reflect.DeepEqual(winner.Vars, stored.Vars) {
		t.Fatalf("winner = %+v; stored = %+v", winner, stored)
	}
}

func TestCompareAndSwapEnvironmentRejectsStaleUpdate(t *testing.T) {
	for _, field := range []string{"vars", "secrets"} {
		t.Run(field, func(t *testing.T) {
			s := open(t)
			initial := &Environment{Name: "sit", Vars: map[string]string{"base": "before"}, Secrets: []string{"apiKey"}}
			if err := s.UpsertEnvironment(initial); err != nil {
				t.Fatal(err)
			}
			expected, err := s.Environment("sit")
			if err != nil {
				t.Fatal(err)
			}
			edited := &Environment{Name: "sit", Vars: map[string]string{"base": "before"}, Secrets: []string{"apiKey"}}
			if field == "vars" {
				edited.Vars["base"] = "newer-edit"
			} else {
				edited.Secrets = []string{"accessToken"}
			}
			if err := s.UpsertEnvironment(edited); err != nil {
				t.Fatal(err)
			}
			imported := &Environment{Name: "sit", Vars: map[string]string{"base": "stale-import"}}
			changed, err := s.CompareAndSwapEnvironment(imported, expected)
			if err != nil || changed {
				t.Fatalf("stale update changed=%t, error=%v", changed, err)
			}
			stored, err := s.Environment("sit")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored.Vars, edited.Vars) || !reflect.DeepEqual(stored.Secrets, edited.Secrets) {
				t.Fatalf("stale import replaced ordinary edit: %+v", stored)
			}
		})
	}
}

func TestCompareAndSwapEnvironmentDetectsDeletion(t *testing.T) {
	s := open(t)
	initial := &Environment{Name: "sit", Vars: map[string]string{"base": "before"}}
	if err := s.UpsertEnvironment(initial); err != nil {
		t.Fatal(err)
	}
	expected, err := s.Environment("sit")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEnvironment("sit"); err != nil {
		t.Fatal(err)
	}
	imported := &Environment{Name: "sit", Vars: map[string]string{"base": "imported"}}
	changed, err := s.CompareAndSwapEnvironment(imported, expected)
	if err != nil || changed {
		t.Fatalf("update after deletion changed=%t, error=%v", changed, err)
	}
	envs, err := s.Environments()
	if err != nil || len(envs) != 0 {
		t.Fatalf("environments after deletion = %v, error=%v", envs, err)
	}
}

func TestCompareAndSwapEnvironmentUpdateKeepsActualID(t *testing.T) {
	s := open(t)
	a := &Environment{Name: "a", Vars: map[string]string{"base": "before"}}
	changed, err := s.CompareAndSwapEnvironment(a, nil)
	if err != nil || !changed {
		t.Fatalf("create a changed=%t, error=%v", changed, err)
	}
	expected, err := s.Environment("a")
	if err != nil {
		t.Fatal(err)
	}
	b := &Environment{Name: "b"}
	if err := s.UpsertEnvironment(b); err != nil {
		t.Fatal(err)
	}
	imported := &Environment{ID: b.ID, Name: "a", Vars: map[string]string{"base": "imported"}, Secrets: []string{"apiKey"}}
	changed, err = s.CompareAndSwapEnvironment(imported, expected)
	if err != nil || !changed {
		t.Fatalf("update a changed=%t, error=%v", changed, err)
	}
	if imported.ID != a.ID || imported.ID == b.ID {
		t.Fatalf("updated a ID = %d; original a = %d, unrelated b = %d", imported.ID, a.ID, b.ID)
	}
	stored, err := s.Environment("a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != a.ID || !reflect.DeepEqual(stored.Vars, imported.Vars) || !reflect.DeepEqual(stored.Secrets, imported.Secrets) {
		t.Fatalf("updated environment = %+v", stored)
	}
}

func TestCompareAndSwapEnvironmentValidatesNames(t *testing.T) {
	s := open(t)
	for _, tc := range []struct {
		name     string
		env      *Environment
		expected *Environment
	}{
		{"nil environment", nil, nil},
		{"empty name", &Environment{}, nil},
		{"mismatched name", &Environment{Name: "sit"}, &Environment{Name: "uat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := s.CompareAndSwapEnvironment(tc.env, tc.expected)
			if err == nil || changed {
				t.Fatalf("invalid input changed=%t, error=%v", changed, err)
			}
		})
	}
}
