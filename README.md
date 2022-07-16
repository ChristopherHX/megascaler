# megascaler
WIP Autoscaler for self-hosted runners. Currently only provides a runner stub.

## Goals
- listen for `workflow_job` webhooks
- create an inmemory runner with a PAT token
- track job assignment, so you can control in which environment the job executes
- cache runner registration token, valid for ca. 1 hour
- cache runner registration jwt, very short lived you can still register ca. > 1000 runners without extra rate limit cost
- use both actions/runner and nektos/act worker
- provide a script to allocate a target enviroment for a job
- provide a script to execute the worker process on the target enviroment, by forwarding stdin

## Non representative Statistics
- 68 of 81 jobs have received the wrong runner
- average time between job start and webhook in_progress is between 1s and 60s
- webhooks cannot always be delivered, need to add github api polling for reliability if waiting long