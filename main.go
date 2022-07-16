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

type JobRequest struct {
	Payload *github.WorkflowJobEvent
}

type workerData struct {
	JobID              int64
	ActualJobRequest   chan *JobRequest
	ExpectedJobRequest *JobRequest
	cancelListener     context.CancelFunc
	Settings           *runnerconfiguration.RunnerSettings
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
	var actualJobRequest *JobRequest
	for {
		b := false
		select {
		case <-wc.JobExecCtx().Done():
			b = true
		case jobRequest := <-arunner.Data.ActualJobRequest:
			actualJobRequest = jobRequest
			b = true
		case <-time.After(time.Second):
			wc.Logger().Update()
			wc.Logger().Log(fmt.Sprintf("%vStill waiting for webhook", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z ")))
		}
		if b {
			break
		}
	}
	if actualJobRequest != nil {
		wc.Logger().Log(fmt.Sprintf("%vGot jobid: %v", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z "), actualJobRequest.Payload.WorkflowJob.ID))
		wc.Logger().Log(fmt.Sprintf("Labels: %v", actualJobRequest.Payload.WorkflowJob.Labels))
		wc.Logger().Log("Event:")
		b, _ := json.MarshalIndent(actualJobRequest.Payload, "", "  ")
		wc.Logger().Log(string(b))
	}
	wc.Logger().Current().Complete("Succeeded")
	wc.Logger().Append(protocol.CreateTimelineEntry(wc.Message().JobID, "__echo", "Echo Hello World"))
	wc.Logger().MoveNext()
	wc.Logger().Log("This is my logger")
	wc.Logger().Log("Hello World")
	for i := 0; i < 200; i++ {
		b := false
		select {
		case <-wc.JobExecCtx().Done():
			b = true
		case <-time.After(time.Second):
			wc.Logger().Log(fmt.Sprintf("%v Dummy work %v", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z "), i))
		}
		if b {
			break
		}
	}
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

func equalFoldSubSet(left []string, right []string) bool {
	for _, l := range left {
		found := false
		for _, r := range right {
			if strings.EqualFold(l, r) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func equalFoldSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	return equalFoldSubSet(left, right) && equalFoldSubSet(right, left)
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
			configureRunner(event, s, nil)
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
			} else {
				fmt.Printf("runner not found %v\n", name)
			}
			if re, ok := s.byJobID.LoadAndDelete(job.GetID()); ok {
				e := re.(*workerData)
				got = e
				if expected.JobID != got.JobID {
					fmt.Println("assigned unexpected job")
					if !equalFoldSet(expected.ExpectedJobRequest.Payload.WorkflowJob.Labels, got.ExpectedJobRequest.Payload.WorkflowJob.Labels) {
						if equalFoldSubSet(got.ExpectedJobRequest.Payload.WorkflowJob.Labels, expected.ExpectedJobRequest.Payload.WorkflowJob.Labels) {
							fmt.Println("unexpected job has less labels than expected job, recreate runner")
							go configureRunner(expected.ExpectedJobRequest.Payload, s, expected)
						}
					}
					go func() {
						rr := &runnerconfiguration.RemoveRunner{}
						rr.Pat = os.Getenv("GITHUB_PAT")
						if got.Settings == nil {
							<-time.After(time.Second * 30)
						}
						if got.Settings != nil {
							fmt.Printf("Removing runner %v\n", got.Settings.Instances[0].Agent.Name)
							rtc, _ := s.tokenCache.LoadOrStore(got.Settings.Instances[0].RegistrationURL, &TokenCache{})
							tc := rtc.(*TokenCache)
							var err error
							auth := tc.GithubAuth
							if auth != nil {
								_, err = rr.Remove(got.Settings, &noSurvey{}, auth)
								if err == nil {
									fmt.Printf("removed with short lived token\n")
								}
							} else {
								err = fmt.Errorf("no cache")
							}
							if err != nil && (err.Error() == "no cache" || strings.Contains(err.Error(), "Http DELETE Request finished 401") || strings.Contains(err.Error(), "Http DELETE Request finished 403")) {
								rr.Token = tc.RunnerToken
								_, err = rr.Remove(got.Settings, &noSurvey{}, nil)
								if err != nil && (strings.Contains(err.Error(), "Http DELETE Request finished 401") || strings.Contains(err.Error(), "Http DELETE Request finished 403")) {
									rr.Token = ""
									_, err = rr.Remove(got.Settings, &noSurvey{}, nil)
									if err != nil {
										fmt.Printf("failed to remove runner: %v\n", err)
									} else {
										fmt.Printf("removed with PAT token\n")
									}
								} else if err != nil {
									fmt.Printf("failed to remove runner: %v\n", err)
								} else {
									fmt.Printf("removed with runner token\n")
								}
							} else if err != nil {
								fmt.Printf("failed to remove runner: %v\n", err)
							}
						} else if err != nil {
							fmt.Println("can't remove runner, settings not set")
						}
					}()

					//got.cancelListener()
					//expected.cancelListener()
				} else {
					fmt.Println("job matched")
				}
			} else {
				fmt.Printf("job not found %v\n", name)
			}
			expected.ActualJobRequest <- got.ExpectedJobRequest
		}
	}
	w.WriteHeader(200)
}

func configureRunner(event *github.WorkflowJobEvent, s *GitHubEventMonitor, previousData *workerData) {
	var err error
	job := event.GetWorkflowJob()
	conf := &runnerconfiguration.ConfigureRunner{}
	conf.Unattended = true
	conf.Labels = job.Labels
	conf.Name = uuid.NewString()
	wd := &workerData{
		JobID: job.GetID(),
		ExpectedJobRequest: &JobRequest{
			Payload: event,
		},
	}
	if previousData != nil {
		wd.ActualJobRequest = previousData.ActualJobRequest
	} else {
		wd.ActualJobRequest = make(chan *JobRequest)
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
				fmt.Println("Used PAT token")
			}
		}()
	}
	wd.Settings = settings

	we := &InMemoryRunner{
		Data: wd,
	}
	run := &actionsrunner.RunRunner{
		Settings: settings,
		Version:  "megascaler-v0.0.0",
	}
	go run.Run(we, listenerctx, context.Background())
}

func main() {
	http.ListenAndServe("0.0.0.0:4032", &GitHubEventMonitor{
		webhookSecretKey: []byte(os.Getenv("GITHUB_SECRET")),
	})
}
