// Package testsupport provides database handles for tests.
//
// Two flavours, because the log lines under test fall into two groups:
//
//   - DeadDB backs the failure paths ("batch insert failed", "log search
//     failed", …). It is a real GORM handle pointed at a closed port with
//     eager pinging disabled, so gorm.Open succeeds and every subsequent query
//     returns a connection error. No server required, so these tests always run.
//   - LiveDB backs the success paths (notably storage's "log retention
//     cleanup", which only logs when rows were actually deleted). Those need a
//     real Postgres, so they are gated on DL_TEST_DSN and skipped without it.
package testsupport

import (
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/thirzq/dockerledger/internal/models"
)

// DSNEnv is the environment variable holding a Postgres DSN for the tests that
// need a live database, e.g.
//
//	DL_TEST_DSN='host=localhost port=5432 user=postgres password=postgres dbname=dockerledger sslmode=disable'
const DSNEnv = "DL_TEST_DSN"

// DeadDB returns a GORM handle whose every query fails with a connection error.
func DeadDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		postgres.Open("host=127.0.0.1 port=1 user=dockerledger password=dockerledger dbname=dockerledger sslmode=disable connect_timeout=1"),
		&gorm.Config{
			// Without this, gorm.Open dials at open time and returns an error
			// instead of a handle we can drive through the repository layer.
			DisableAutomaticPing: true,
			Logger:               gormlogger.Discard,
		},
	)
	if err != nil {
		t.Fatalf("open dead db handle: %v", err)
	}
	return db
}

// LiveDB returns a migrated connection to the Postgres named by DL_TEST_DSN,
// skipping the test when the variable is unset.
func LiveDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(DSNEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping test that needs a live Postgres", DSNEnv)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("connect to %s: %v", DSNEnv, err)
	}
	if err := db.AutoMigrate(&models.Container{}, &models.LogEntry{}, &models.WakeProxyState{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}
