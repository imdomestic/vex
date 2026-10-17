package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/imdomestic/vex/controller/internal/migration"
)

func migrateCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "chorus" {
		return fmt.Errorf("usage: vexd migrate chorus --from-db PATH --from-dir PATH --project ID [--dry-run]")
	}
	flags := flag.NewFlagSet("migrate chorus", flag.ContinueOnError)
	var options migration.Options
	flags.StringVar(&options.FromDB, "from-db", "", "legacy database")
	flags.StringVar(&options.FromDir, "from-dir", "", "legacy data directory")
	flags.StringVar(&options.ToDir, "to-dir", "/srv/vex", "Vex data directory")
	flags.StringVar(&options.Project, "project", "", "destination project ID")
	flags.StringVar(&options.Zone, "zone", "vex.internal", "room DNS zone")
	flags.StringVar(&options.UsersFile, "users-file", "", "legacy token file; only SHA256 values are emitted")
	dry := flags.Bool("dry-run", false, "print the verified migration plan")
	resume := flags.String("resume", "", "resume the persistent migration journal")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected migration arguments")
	}
	if *resume != "" {
		count := 0
		flags.Visit(func(*flag.Flag) { count++ })
		if count != 1 {
			return fmt.Errorf("--resume must be used alone")
		}
		plan, err := migration.Resume(ctx, *resume)
		if err != nil {
			return err
		}
		fmt.Printf("migrated project=%s records=%d database=%s journal=%s\n", plan.Project, len(plan.Records), plan.DestinationDB, migration.JournalPath(plan))
		return nil
	}
	plan, err := migration.Prepare(ctx, options)
	if err != nil {
		return err
	}
	if *dry {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(plan)
	}
	if err = migration.Apply(ctx, plan); err != nil {
		return err
	}
	fmt.Printf("migrated project=%s records=%d database=%s journal=%s\n", plan.Project, len(plan.Records), plan.DestinationDB, migration.JournalPath(plan))
	return nil
}
