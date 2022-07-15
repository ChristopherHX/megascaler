package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

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
	JobID          int64
	actualJobID    chan int64
	payload        *github.WorkflowJobEvent
	cancelListener context.CancelFunc
	labels         []string
}

type TokenCache struct {
	RunnerToken string
	GithubAuth  *protocol.GitHubAuthResult
	Lock        sync.Mutex
}

type GitHubEventMonitor struct {
	webhookSecretKey []byte
	byJobID          sync.Map
	byWorkerName     sync.Map
	tokenCache       sync.Map
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
	wc.Logger().Log(fmt.Sprintf("%vExpect jobid: %v", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z "), arunner.Data.JobID))
	var actualJobid int64
	for {
		b := false
		select {
		case jobid := <-arunner.Data.actualJobID:
			actualJobid = jobid
			b = true
		case <-time.After(time.Second):
			wc.Logger().Update()
			wc.Logger().Log(fmt.Sprintf("%vStill waiting for webhook", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z ")))
		}
		if b {
			break
		}
	}
	wc.Logger().Log(fmt.Sprintf("%vGot jobid: %v", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z "), actualJobid))
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
				labels:      job.Labels,
			}
			var listenerctx context.Context
			listenerctx, wd.cancelListener = context.WithCancel(context.Background())

			s.byWorkerName.Store(conf.Name, wd)
			s.byJobID.Store(job.GetID(), wd)
			conf.NoDefaultLabels = true

			conf.URL = event.GetRepo().GetHTMLURL()
			conf.Pat = os.Getenv("GITHUB_PAT")
			conf.Ephemeral = true
			rtc, _ := s.tokenCache.LoadOrStore(conf.URL, &TokenCache{})
			cl := &http.Client{}
			tc := rtc.(*TokenCache)
			var settings *runnerconfiguration.RunnerSettings
			auth := tc.GithubAuth
			if auth != nil {
				csettings, err := conf.Configure(&runnerconfiguration.RunnerSettings{}, &noSurvey{}, auth)
				if err == nil {
					fmt.Println("Successfully reused short lived auth")
					settings = csettings
				}
			}
			if settings == nil {
				func() {
					tc.Lock.Lock()
					defer tc.Lock.Unlock()
					if len(tc.RunnerToken) > 0 {
						conf.Token = tc.RunnerToken
						auth, err = conf.Authenicate(cl, &noSurvey{})
					} else {
						err = fmt.Errorf("not cached")
					}
					if err == nil {
						fmt.Println("Successfully reused runner token")
						tc.GithubAuth = auth
						settings, _ = conf.Configure(&runnerconfiguration.RunnerSettings{}, &noSurvey{}, auth)
					} else {
						conf.Token = ""
						auth, _ := conf.Authenicate(cl, &noSurvey{})
						tc.RunnerToken = conf.Token
						tc.GithubAuth = auth
						settings, _ = conf.Configure(&runnerconfiguration.RunnerSettings{}, &noSurvey{}, auth)
					}
				}()
			}

			we := &InMemoryRunner{
				Data: wd,
			}
			run := &actionsrunner.RunRunner{
				Settings: settings,
				Version:  "megascaler-v0.0.0",
			}
			go run.Run(we, listenerctx, context.Background())
		} else if strings.EqualFold(event.GetAction(), "in_progress") {
			fmt.Println("runner in progress")
			job := event.GetWorkflowJob()
			name := job.GetRunnerName()
			var expected *workerData
			var got *workerData
			if re, ok := s.byWorkerName.LoadAndDelete(name); ok {
				e := re.(*workerData)
				expected = e
				fmt.Println("runner matched")
				e.actualJobID <- job.GetID()
				e.payload = event
			} else {
				fmt.Printf("runner not found %v\n", name)
			}
			if re, ok := s.byJobID.LoadAndDelete(job.GetID()); ok {
				e := re.(*workerData)
				got = e
				if expected.JobID != got.JobID {
					fmt.Println("assigned unexpected job")
					if len(expected.labels) != len(got.labels) {
						expected.cancelListener()
					}
				} else {
					fmt.Println("job matched")
				}
			} else {
				fmt.Printf("job not found %v\n", name)
			}

		}

	}
	w.WriteHeader(200)
}

func main() {
	http.ListenAndServe("0.0.0.0:4032", &GitHubEventMonitor{
		webhookSecretKey: []byte(os.Getenv("GITHUB_SECRET")),
	})
}
