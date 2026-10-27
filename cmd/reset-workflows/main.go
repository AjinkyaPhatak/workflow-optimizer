// Command reset-workflows explicitly clears workflow state in an offline local
// development stack. Preview is the default; it is never invoked by setup.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/client"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/maintenance"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func backupValid(path string) error {
	if path == "" {
		return errors.New("--backup must identify a recent PostgreSQL custom-format dump")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 64 {
		return errors.New("backup is missing or empty")
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("backup must be private: chmod 600 the dump file")
	}
	if time.Since(info.ModTime()) > time.Hour {
		return errors.New("backup is older than one hour; create a fresh dump after stopping the application")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("backup cannot be read")
	}
	defer f.Close()
	header := make([]byte, 5)
	if _, err = io.ReadFull(f, header); err != nil || string(header) != "PGDMP" {
		return errors.New("backup must be a PostgreSQL custom-format dump (pg_dump -Fc)")
	}
	return nil
}
func run() error {
	execute := flag.Bool("execute", false, "perform the reset; default only previews counts")
	database := flag.String("database", "", "exact local database name to reset (required with --execute)")
	backup := flag.String("backup", "", "fresh private custom-format PostgreSQL dump (required with --execute)")
	flag.Parse()
	dbURL, redisURL := os.Getenv("DATABASE_URL"), os.Getenv("REDIS_URL")
	temporalAddress := env("TEMPORAL_ADDRESS", "127.0.0.1:57233")
	if err := maintenance.ValidateLocalURL(dbURL); err != nil {
		return fmt.Errorf("DATABASE_URL: %w", err)
	}
	if err := maintenance.ValidateLocalURL(redisURL); err != nil {
		return fmt.Errorf("REDIS_URL: %w", err)
	}
	if err := maintenance.ValidateLocalAddress(temporalAddress); err != nil {
		return err
	}
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return errors.New("DATABASE_URL could not be parsed")
	}
	cfg.MaxConns = 1
	if *execute {
		if *database == "" || *database != cfg.ConnConfig.Database {
			return errors.New("--database must exactly match the configured local database")
		}
		if err := backupValid(*backup); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("could not create database connection")
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		return errors.New("could not connect to the local database")
	}
	if *execute {
		if err = maintenance.WaitOffline(ctx, func(ctx context.Context) error { return maintenance.AssertOffline(ctx, pool) }); err != nil {
			return err
		}
	}
	snapshot, err := maintenance.Inspect(ctx, pool)
	if err != nil {
		return errors.New("could not inspect local workflow tables")
	}
	if snapshot.Schema != "public" {
		return errors.New("command only operates on the local public application schema")
	}
	ropts, err := redis.ParseURL(redisURL)
	if err != nil {
		return errors.New("REDIS_URL could not be parsed")
	}
	r := redis.NewClient(ropts)
	defer r.Close()
	if err = r.Ping(ctx).Err(); err != nil {
		return errors.New("could not connect to local Redis")
	}
	namespace, taskQueue := env("TEMPORAL_NAMESPACE", "default"), env("TEMPORAL_TASK_QUEUE", "workflow-optimizer-v2")
	tc, err := client.DialContext(ctx, client.Options{HostPort: temporalAddress, Namespace: namespace})
	if err != nil {
		return errors.New("could not connect to local Temporal")
	}
	defer tc.Close()
	inventory, err := maintenance.InspectTemporal(ctx, tc, namespace, taskQueue)
	if err != nil {
		return errors.New("could not inventory application Temporal executions and schedules")
	}
	if err = maintenance.IncludeKnownTemporal(ctx, tc, taskQueue, &inventory, snapshot.AutomationIDs, snapshot.AutomationRunIDs); err != nil {
		return fmt.Errorf("could not verify known Temporal ownership: %w", err)
	}
	report := struct {
		Database   string                            `json:"database"`
		Schema     string                            `json:"schema"`
		Workflow   map[string]maintenance.TableState `json:"workflow_tables"`
		Protected  map[string]maintenance.TableState `json:"preserved_tables"`
		Schedules  int                               `json:"temporal_schedules"`
		Executions int                               `json:"temporal_executions"`
	}{cfg.ConnConfig.Database, snapshot.Schema, snapshot.Workflow, snapshot.Protected, len(inventory.Schedules), len(inventory.Executions)}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(encoded))
	if !*execute {
		fmt.Println("Preview only. Stop all application processes, create a private pg_dump -Fc backup, then supply --execute --database NAME --backup FILE.")
		return nil
	}
	if err = maintenance.CleanTemporal(ctx, tc, namespace, taskQueue, inventory); err != nil {
		return fmt.Errorf("Temporal cleanup did not finish; database is unchanged: %w", err)
	}
	if err = maintenance.AssertOffline(ctx, pool); err != nil {
		return err
	}
	removed, err := maintenance.CleanRedis(ctx, r, env("REDIS_QUEUE_NAME", config.DefaultRedisQueueName), env("REDIS_DEAD_LETTER_QUEUE", config.DefaultRedisDeadLetterQueue), snapshot.ExecutionIDs)
	if err != nil {
		return errors.New("queue cleanup did not finish; database is unchanged")
	}
	if err = maintenance.ClearDatabase(ctx, pool, snapshot); err != nil {
		return fmt.Errorf("database reset rolled back: %w", err)
	}
	after, err := maintenance.Inspect(ctx, pool)
	if err != nil {
		return errors.New("reset committed but final inspection failed")
	}
	for name, state := range after.Workflow {
		if state.Count != 0 {
			return fmt.Errorf("reset verification failed: %s is not empty", name)
		}
	}
	fmt.Printf("Workflow state cleared. All protected records verified unchanged. Removed %d matching Redis entries. Backup: %s\n", removed, filepath.Clean(*backup))
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Workflow reset:", err)
		os.Exit(1)
	}
}
