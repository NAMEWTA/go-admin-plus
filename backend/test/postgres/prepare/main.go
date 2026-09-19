// Command prepare creates one isolated schema for a required PostgreSQL CI suite.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var schemaPattern = regexp.MustCompile(`\Aci_[a-z0-9_]{1,50}\z`)

func main() {
	schema := flag.String("schema", "", "isolated CI schema name")
	flag.Parse()
	if !schemaPattern.MatchString(*schema) || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "POSTGRES_SCHEMA_PREPARE_FAIL invalid schema")
		os.Exit(2)
	}
	dsn := os.Getenv("GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "POSTGRES_SCHEMA_PREPARE_FAIL disposable DSN is required")
		os.Exit(2)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "POSTGRES_SCHEMA_PREPARE_FAIL open")
		os.Exit(1)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA "`+*schema+`"`); err != nil {
		fmt.Fprintln(os.Stderr, "POSTGRES_SCHEMA_PREPARE_FAIL create")
		os.Exit(1)
	}
	fmt.Println("POSTGRES_SCHEMA_PREPARE_PASS")
}
