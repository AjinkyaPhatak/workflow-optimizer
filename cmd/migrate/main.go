// Command migrate applies pending SQL migrations. API and worker startup do not migrate.
package main

import (
	"flag"
	"log"
	"os"

	"workflow-optimizer/internal/infrastructure/postgres"
)

func main() {
	dir := flag.String("path", "migrations", "directory containing SQL migrations")
	flag.Parse()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if err := postgres.ApplyMigrations(url, *dir); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Print("database migrations are up to date")
}
