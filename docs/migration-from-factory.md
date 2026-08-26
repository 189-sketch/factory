# Migrate from Factory

Machinist is a replacement for Factory, not an in-place upgrade. The public Factory
project and the private `factory-v2` preview both used `~/.factory`, but their configuration
and SQLite formats are not compatible with each other. Machinist never reads, moves, or
deletes that directory automatically.

## Start clean

1. Stop every Factory server and worker. Finish or cancel queued work before continuing.
2. Back up `~/.factory` if you need its configuration, run history, or database.
3. Build Machinist and run `machinist init`. This creates a separate `~/.machinist`
   directory.
4. Recreate repository paths, executor commands, model aliases, agents, and pipelines in
   the new files. Do not copy a public Factory database into Machinist.
5. Update custom integrations to use the new names below.

| Factory preview | Machinist |
| --- | --- |
| `factory` | `machinist` |
| `~/.factory` | `~/.machinist` |
| `{{factory.prompt}}` | `{{machinist.prompt}}` |
| `{{factory.model}}` | `{{machinist.model}}` |
| `FACTORY_RUN_ID` | `MACHINIST_RUN_ID` |
| `FACTORY_REPOSITORY` | `MACHINIST_REPOSITORY` |
| `FACTORY_TOKEN_USAGE_PATH` | `MACHINIST_TOKEN_USAGE_PATH` |
| `--factory-config` | `--machinist-config` |
| `X-Factory-CSRF` | `X-Machinist-CSRF` |
| `factory:*` issue labels | `machinist:*` issue labels |

The private preview database may be copied only after a clean shutdown and only if there
are no queued or running jobs. Rename the copied file to `machinist.db` and update the
server configuration. Already rendered prompts and historical output keep their original
content. A fresh database is safer when preserving preview history is not important.

## Roll back

Keep the backup and old binary until Machinist has completed a real run. The final public
Factory source remains available from the `factory-legacy-final` tag in the renamed public
repository.
