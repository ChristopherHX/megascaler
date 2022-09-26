# megascaler
WIP Autoscaler for self-hosted runners.

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
- `${{ github.megascaler.labels }}` is assigned to an array of string with all labels from `runs-on` of the current instance
- `${{ github.megascaler.variables }}` is assigned to the otherwise hidden system variables of Actions
- `${{ github.megascaler.jobDisplayName }}` is assigned to the otherwise hidden job display name

## Non representative Statistics
- 68 of 81 jobs have received the wrong runner
- average time between job start and webhook in_progress is between 1s and 60s
