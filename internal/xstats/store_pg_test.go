package xstats

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/openpak/x360-common/pgstate"
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
