package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ChristopherHX/github-act-runner/actionsrunner"
	"github.com/ChristopherHX/github-act-runner/protocol"
	"github.com/ChristopherHX/github-act-runner/runnerconfiguration"
	"github.com/google/go-github/v45/github"
	"github.com/google/uuid"
)

type noSurvey struct {
}

func (*noSurvey) GetInput(prompt string, def string) string {
	return def
}
func (*noSurvey) GetSelectInput(prompt string, options []string, def string) string {
	return def
}
func (*noSurvey) GetMultiSelectInput(prompt string, options []string) []string {
	return []string{}
}

type workerData struct {
	JobID       int64
	actualJobID chan int64
	payload     *github.WorkflowJobEvent
}

type GitHubEventMonitor struct {
	webhookSecretKey []byte
	worker           map[string]*workerData
}

type InMemoryRunner struct {
	actionsrunner.ConsoleLogger
	Data *workerData
}

func (arunner *InMemoryRunner) WriteJson(path string, value interface{}) error {
	return nil
}

func (arunner *InMemoryRunner) ReadJson(path string, value interface{}) error {
	return os.ErrNotExist
}

func (arunner *InMemoryRunner) Remove(fname string) error {
	return nil
}

func (arunner *InMemoryRunner) ExecWorker(run *actionsrunner.RunRunner, wc actionsrunner.WorkerContext, jobreq *protocol.AgentJobRequestMessage, src []byte) error {
	wc.Logger().Log(fmt.Sprintf("Expect jobid: %v", arunner.Data.JobID))
	actualJobid := <-arunner.Data.actualJobID
	wc.Logger().Log(fmt.Sprintf("Got jobid: %v", actualJobid))
	wc.Logger().Log(fmt.Sprintf("Labels: %v", arunner.Data.payload.WorkflowJob.Labels))
	wc.Logger().Log("Event:")
	b, _ := json.MarshalIndent(arunner.Data.payload, "", "  ")
	wc.Logger().Log(string(b))
	wc.Logger().Current().Complete("Succeeded")
	wc.Logger().Append(protocol.CreateTimelineEntry(wc.Message().JobID, "__echo", "Echo Hello World"))
	wc.Logger().MoveNext()
	wc.Logger().Log("This is my logger")
	wc.Logger().Log("Hello World")
	wc.Logger().Current().Complete("Succeeded")
	wc.Logger().MoveNext()
	wc.Logger().Logger.Close()
	wc.Logger().TimelineRecords.Value[0].Complete("Succeeded")
	wc.Logger().Finish()
	wc.FinishJob("Succeeded", &map[string]protocol.VariableValue{
		"Result1": {
			Value: "Hello World",
		},
	})
	return nil //fmt.Errorf("Not implemented")
}

func (s *GitHubEventMonitor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	payload, err := github.ValidatePayload(r, s.webhookSecretKey)
	if err != nil {
		return
	}
	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		return
	}
	switch event := event.(type) {
	case *github.WorkflowJobEvent:
		if strings.EqualFold(event.GetAction(), "queued") {
			fmt.Println("config runner")
			job := event.GetWorkflowJob()
			conf := &runnerconfiguration.ConfigureRunner{}
			conf.Unattended = true
			conf.Labels = job.Labels
			conf.Name = uuid.NewString()
			wd := &workerData{
				JobID:       job.GetID(),
				actualJobID: make(chan int64),
			}
			s.worker[conf.Name] = wd
			conf.NoDefaultLabels = true

			conf.URL = event.GetRepo().GetHTMLURL()
			conf.Pat = os.Getenv("GITHUB_PAT")
			conf.Ephemeral = true
			settings, _ := conf.Configure(&runnerconfiguration.RunnerSettings{}, &noSurvey{}, nil)

			we := &InMemoryRunner{
				Data: wd,
			}
			run := &actionsrunner.RunRunner{
				Settings: settings,
				Version:  "megascaler-v0.0.0",
			}
			go run.Run(we, context.Background(), context.Background())
		} else if strings.EqualFold(event.GetAction(), "in_progress") {
			fmt.Println("runner in progress")
			job := event.GetWorkflowJob()
			name := job.GetRunnerName()
			if e, ok := s.worker[name]; ok {
				fmt.Println("runner matched")
				e.actualJobID <- job.GetID()
				e.payload = event
			} else {
				fmt.Printf("runner not found %v\n", name)
			}

		}

	}
	w.WriteHeader(200)
}

func main() {
	http.ListenAndServe("0.0.0.0:4032", &GitHubEventMonitor{
		webhookSecretKey: []byte(os.Getenv("GITHUB_SECRET")),
		worker:           make(map[string]*workerData),
	})
}
