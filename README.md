# megascaler
WIP Autoscaler for self-hosted runners.

This program will communticate with GitHub to receive job requests and will delegate running the job to a worker program, while the worker program has no access to the runner / PAT tokens if it is spawned in an isolated environment.

Works with both the official https://github.com/actions/runner and the unofficial https://github.com/ChristopherHX/github-act-runner.

## Known problems
- you should avoid using multiple labels
  - if you really want to use them always provide an entry for any possible configuration
  - your jobs with more labels may wait longer for execution, due to other jobs taking away your runner
  - might need to create runners with more labels than requested to minimize queue time
- webhooks cannot always be delivered, need to add github api polling for reliability if waiting long

## Goals
- listen for `workflow_job` webhooks
- create an inmemory runner with a PAT token
- track job assignment, so you can control in which environment the job executes
- cache runner registration token, valid for ca. 1 hour
- cache runner registration jwt, very short lived you can still register ca. > 1000 runners without extra rate limit cost
- use both actions/runner and nektos/act worker
- provide a script to allocate a target enviroment for a job
- provide a script to execute the worker process on the target enviroment, by forwarding stdin

## Extras for actions/runner
- `${{ github.megascaler.jobid }}` is assigned to the rest api job id
- `${{ github.megascaler.job_url }}` is assigned to the job html url
- `${{ github.megascaler.run_url }}` is assigned to the workflow run html url
- `${{ github.megascaler.labels }}` is assigned to an array of string with all labels from `runs-on` of the current instance
- `${{ github.megascaler.variables }}` is assigned to the otherwise hidden system variables of Actions
- `${{ github.megascaler.jobDisplayName }}` is assigned to the otherwise hidden job display name

## Non representative Statistics
- 68 of 81 jobs have received the wrong runner
- average time between job start and webhook in_progress is between 1s and 60s

## Getting started

create a `config.yml`
```yaml
worker:                                             # list of worker to spawn if the label list has an exact match
- labels:
  - github-act-runner                               # labels to spawn the specfic worker, if this matches what you added to the `runs-on` key this program will spawn a worker
  args:
  - /workspaces/megascaler/runner/github-act-runner # Path to https://github.com/ChristopherHX/github-act-runner binary
  - worker                                          # Execute in worker mode
- labels:
  - actions-runner                                  # labels to spawn the specfic worker, if this matches what you added to the `runs-on` key this program will spawn a worker
  args:                                             # create a `/workspaces/megascaler/runner/.runner` textfile with content `{"workFolder": "_work"}`
  - pwsh                                            # Path to https://github.com/powershell/powershell binary or add it to your `PATH` env variable
  - actions-runner-worker.ps1                       # Get the file from https://github.com/ChristopherHX/github-act-runner/blob/main/compat/actions-runner-worker.ps1
  - /workspaces/megascaler/runner/bin/Runner.Worker # Path to https://github.com/actions/runner binary
address: 0.0.0.0:9403                               # webhook listener to receive `workflow_job` events from GitHub
# secret: mysecret                                  # validate the webhook signature to harden the webhook endpoint
pat: ghp_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX       # GitHub Classic PAT with full `repo` scope
```

You can also pipe the worker via the `args` list via ssh or other programs, it will connect to GitHub via http and communicates over stdin/stdout.

run
```
go build
./megascaler
```

or debug `main.go`