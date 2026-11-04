package xstats

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/openpak/x360-common/pgstate"
	"github.com/openpak/x360-xstats/internal/fakeleaderboards"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("X360_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("X360_TEST_DATABASE_URL not set")
	}
	return dsn
}

// pgSchema returns a private schema name, dropped when the test ends.
func pgSchema(t *testing.T) string {
	dsn := testDSN(t)
	b := make([]byte, 6)
	rand.Read(b)
	schema := "t_xstats_" + hex.EncodeToString(b)
	t.Cleanup(func() {
		if p, err := pgstate.Open(context.Background(), dsn, schema); err == nil {
			p.Pool().Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
			p.Close()
		}
	})
	return schema
}

// testOpen opens (or reopens, a restart) the store in schema.
func testOpen(t *testing.T, schema string) *Store {
	t.Helper()
	s, err := Open(context.Background(), testDSN(t), schema, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestOpenRefusesWithoutDSN(t *testing.T) {
	if _, err := Open(context.Background(), "", Schema, ""); err == nil {
		t.Fatal("started without a database URL")
	}
}

func TestLegacyFileImportedOnce(t *testing.T) {
	file := filepath.Join(t.TempDir(), "stats.json")
	js := `{"views":{"00000001/1":{"0009000000000001":{"cols":{},"seq":1}}},"next":2}`
	if err := os.WriteFile(file, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	schema := pgSchema(t)
	s, err := Open(context.Background(), testDSN(t), schema, file)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.Remove(file)
	s = testOpen(t, schema)
	if len(s.Views["00000001/1"]) != 1 || s.Next != 2 {
		t.Fatalf("not imported: %+v", s.Views)
	}
}

// TestMigrationSurvivesRestart: after the rows move to the core, a restart (the store reopened
// from Postgres) still holds the old rows, knows they are migrated, and migrates nothing again.
func TestMigrationSurvivesRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "stats.json")
	js := `{"views":{"4D5307E6/1":{"0009000000000001":{"gamertag":"Alpha","cols":{"65534":{"t":4,"v":100}},"seq":1}}},"next":2}`
	if err := os.WriteFile(file, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	schema := pgSchema(t)
	s, err := Open(context.Background(), testDSN(t), schema, file)
	if err != nil {
		t.Fatal(err)
	}
	f := fakeleaderboards.New(lbKey)
	h := newHandler(t, f, s)
	st, err := h.MigrateLegacy(context.Background())
	if err != nil || st.Migrated != 1 {
		t.Fatalf("%v %+v", err, st)
	}
	s.Close()

	s2 := testOpen(t, schema)
	if !s2.Migrated || len(s2.Views["4D5307E6/1"]) != 1 {
		t.Fatalf("after restart: migrated=%v views=%v", s2.Migrated, s2.Views)
	}
	st, err = newHandler(t, f, s2).MigrateLegacy(context.Background())
	if err != nil || st.Rows != 0 {
		t.Fatalf("second run: %v %+v", err, st)
	}
}

func TestEraseRemovesProfileAndLegacyRowsOfThoseXUIDsOnly(t *testing.T) {
	s := NewMemoryStore()
	const a, b = 0x0009000000000001, 0x0009000000000002
	s.Profiles = map[string]*Profile{xuidKey(a): {}, xuidKey(b): {}}
	s.Views["0000FFFE/1"] = map[string]*Row{xuidKey(a): {Gamertag: "A"}, xuidKey(b): {Gamertag: "B"}}
	if n, err := s.Erase([]uint64{a}); err != nil || n != 2 {
		t.Fatalf("erase: %d %v", n, err)
	}
	if n, err := s.Erase([]uint64{a}); err != nil || n != 0 {
		t.Fatalf("second erase: %d %v", n, err)
	}
	if s.Profiles[xuidKey(b)] == nil || s.Views["0000FFFE/1"][xuidKey(b)] == nil || len(s.Profiles) != 1 || len(s.Views["0000FFFE/1"]) != 1 {
		t.Fatal("wrong records erased")
	}
}
