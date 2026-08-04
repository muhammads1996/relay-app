package store

import (
	"testing"
	"time"

	"github.com/muhaymien96/relay/internal/dsl"
)

// seedRequest creates a collection + request so test cases have something to
// attach to, and returns the request id.
func seedRequest(t *testing.T, s *Store) int64 {
	t.Helper()
	col := &Collection{Name: "C"}
	if err := s.CreateCollection(col); err != nil {
		t.Fatal(err)
	}
	req := &Request{
		CollectionID: col.ID,
		Spec: &dsl.Request{
			Name: "Health", Method: "GET", URL: "{{baseUrl}}/health",
			Assertions: []dsl.Assertion{{Type: "status", Equals: int64(200)}},
		},
	}
	if err := s.CreateRequest(req); err != nil {
		t.Fatal(err)
	}
	return req.ID
}

// TestTestSetsNoDeadlock guards against a regression where TestSets() issued a
// nested query (testSetIDs) while the outer rows were still open. With the
// single-connection pool (SetMaxOpenConns(1)) that deadlocks the whole server.
// The whole flow is run under a timeout so a regression fails loudly instead of
// hanging CI forever.
func TestTestSetsNoDeadlock(t *testing.T) {
	s := open(t)
	reqID := seedRequest(t, s)

	// EnsureDefaultTestCases seeds one default case for the request.
	tc := &TestCase{RequestID: reqID, Name: "case A", Enabled: true}
	if err := s.CreateTestCase(tc); err != nil {
		t.Fatal(err)
	}

	set := &TestSet{Name: "Regression", TestIDs: []int64{tc.ID}}
	if err := s.CreateTestSet(set); err != nil {
		t.Fatal(err)
	}

	done := make(chan []TestSet, 1)
	errc := make(chan error, 1)
	go func() {
		sets, err := s.TestSets()
		if err != nil {
			errc <- err
			return
		}
		done <- sets
	}()

	select {
	case err := <-errc:
		t.Fatalf("TestSets: %v", err)
	case sets := <-done:
		if len(sets) != 1 {
			t.Fatalf("want 1 test set, got %d", len(sets))
		}
		if len(sets[0].TestIDs) != 1 || sets[0].TestIDs[0] != tc.ID {
			t.Fatalf("test set members = %v, want [%d]", sets[0].TestIDs, tc.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TestSets() deadlocked: nested query while outer rows open (SetMaxOpenConns(1))")
	}
}

// TestTestSetCreateSkipsUnknownIDs verifies that referencing a test ID that
// doesn't exist no longer crashes with a foreign-key error and does not leave
// an orphaned, empty test set behind.
func TestTestSetCreateSkipsUnknownIDs(t *testing.T) {
	s := open(t)
	reqID := seedRequest(t, s)
	tc := &TestCase{RequestID: reqID, Name: "real", Enabled: true}
	if err := s.CreateTestCase(tc); err != nil {
		t.Fatal(err)
	}

	// 999999 does not exist; it should be skipped, not error.
	set := &TestSet{Name: "Mixed", TestIDs: []int64{tc.ID, 999999}}
	if err := s.CreateTestSet(set); err != nil {
		t.Fatalf("CreateTestSet should skip unknown IDs, got: %v", err)
	}

	got, err := s.TestSet(set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TestIDs) != 1 || got.TestIDs[0] != tc.ID {
		t.Fatalf("members = %v, want only [%d]", got.TestIDs, tc.ID)
	}

	sets, err := s.TestSets()
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 {
		t.Fatalf("want exactly 1 set (no orphans), got %d", len(sets))
	}
}

// TestTestSetMembershipRoundTrip exercises create/update/replace of the
// many-to-many test_set_tests linkage.
func TestTestSetMembershipRoundTrip(t *testing.T) {
	s := open(t)
	reqID := seedRequest(t, s)

	a := &TestCase{RequestID: reqID, Name: "A", Enabled: true}
	b := &TestCase{RequestID: reqID, Name: "B", Enabled: true}
	if err := s.CreateTestCase(a); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTestCase(b); err != nil {
		t.Fatal(err)
	}

	set := &TestSet{Name: "S", TestIDs: []int64{a.ID}}
	if err := s.CreateTestSet(set); err != nil {
		t.Fatal(err)
	}

	got, err := s.TestSet(set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TestIDs) != 1 || got.TestIDs[0] != a.ID {
		t.Fatalf("members = %v, want [%d]", got.TestIDs, a.ID)
	}

	// Replace membership with both tests.
	set.TestIDs = []int64{a.ID, b.ID}
	if err := s.UpdateTestSet(set); err != nil {
		t.Fatal(err)
	}
	got, _ = s.TestSet(set.ID)
	if len(got.TestIDs) != 2 {
		t.Fatalf("after update members = %v, want 2", got.TestIDs)
	}

	// Deleting a test case cascades to membership.
	if err := s.DeleteTestCase(b.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.TestSet(set.ID)
	if len(got.TestIDs) != 1 || got.TestIDs[0] != a.ID {
		t.Fatalf("after delete members = %v, want [%d]", got.TestIDs, a.ID)
	}
}
