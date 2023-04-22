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
	yaml "gopkg.in/yaml.v3"
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
	Payload    *github.WorkflowJobEvent
	WorkerArgs []string
}

type workerData struct {
	JobID              int64
	WorkerName         string
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
	byJobID      sync.Map
	byWorkerName sync.Map
	tokenCache   sync.Map
	Config       *Config
}

type InMemoryRunner struct {
	actionsrunner.ConsoleLogger
	Data   *workerData
	Config *Config
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

type MegaScalerContextData struct {
	Labels []string
	JobID  string
}

func (arunner *InMemoryRunner) ExecWorker(run *actionsrunner.RunRunner, wc actionsrunner.WorkerContext, jobreq *protocol.AgentJobRequestMessage, src []byte) error {
	wc.Logger().Log(fmt.Sprintf("%vExpect jobid: %v", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z "), arunner.Data.JobID))
	var actualJobRequest *JobRequest
	queryWebhookTimeout := time.After(5 * time.Minute)
	for {
		b := false
		select {
		case <-wc.JobExecCtx().Done():
			b = true
		case jobRequest := <-arunner.Data.ActualJobRequest:
			actualJobRequest = jobRequest
			b = true
		case <-time.After(5 * time.Second):
			wc.Logger().Update()
			wc.Logger().Log(fmt.Sprintf("%vStill waiting for webhook", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z ")))
		case <-queryWebhookTimeout:
			wc.Logger().Update()
			wc.Logger().Log(fmt.Sprintf("%v##[Error]No matching webhook received within 5 Minutes, killing runner", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z ")))
			wc.Logger().Current().Complete("Failed")
			wc.Logger().Update()
			wc.Logger().TimelineRecords.Value[0].Complete("Failed")
			wc.Logger().Finish()
			wc.FinishJob("Failed", &map[string]protocol.VariableValue{})
		}
		if b {
			break
		}
	}
	we := &actionsrunner.WorkerRunnerEnvironment{}
	if actualJobRequest != nil {
		wc.Logger().Log(fmt.Sprintf("%vGot jobid: %v", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z "), *actualJobRequest.Payload.WorkflowJob.ID))
		wc.Logger().Log(fmt.Sprintf("Labels: %v", actualJobRequest.Payload.WorkflowJob.Labels))
		wc.Logger().Log("Event:")
		b, _ := json.MarshalIndent(actualJobRequest.Payload, "", "  ")
		wc.Logger().Log(string(b))
		we.WorkerArgs = actualJobRequest.WorkerArgs
		wc.Logger().Current().Complete("Succeeded")
	} else {
		wc.Logger().Log(fmt.Sprintf("%v##[Error] Couldn't find", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z ")))
		wc.Logger().Current().Complete("Failed")
		wc.Logger().Logger.Close()
		wc.Logger().TimelineRecords.Value[0].Complete("Failed")
		wc.Logger().Finish()
		wc.FinishJob("Failed", &map[string]protocol.VariableValue{})
		return nil
	}
	var rawjobreq map[string]interface{}
	err := json.Unmarshal(src, &rawjobreq)
	if err != nil {
		return err
	}

	clabels := []protocol.PipelineContextData{}
	var str int32
	var array int32 = 1
	var dictionary int32 = 2
	var number int32 = 4
	for _, l := range actualJobRequest.Payload.WorkflowJob.Labels {
		cl := l
		clabels = append(clabels, protocol.PipelineContextData{
			Type: &str,
			StringValue: &cl,
		})
	}
	var jobidfl float64 = float64(*actualJobRequest.Payload.WorkflowJob.ID)
	megascalerContextData := []protocol.DictionaryContextDataPair{
		{Key: "labels", Value: protocol.PipelineContextData{
			Type:       &array,
			ArrayValue: &clabels,
		}},
		{
			Key: "jobid", Value: protocol.PipelineContextData{
				Type:        &number,
				NumberValue: &jobidfl,
			},
		},
		{
			Key: "job_url", Value: protocol.PipelineContextData{
				Type:        &str,
				StringValue: actualJobRequest.Payload.WorkflowJob.HTMLURL,
			},
		},
		{
			Key: "run_url", Value: protocol.PipelineContextData{
				Type:        &str,
				StringValue: actualJobRequest.Payload.WorkflowJob.RunURL,
			},
		},
	}
	if len(jobreq.JobDisplayName) > 0 {
		megascalerContextData = append(megascalerContextData, protocol.DictionaryContextDataPair{
			Key: "JobDisplayName", Value: protocol.PipelineContextData{
				Type:        &str,
				StringValue: &jobreq.JobDisplayName,
			},
		})
	}
	if len(jobreq.Variables) > 0 {
		vars := []protocol.DictionaryContextDataPair{}
		for k, v := range jobreq.Variables {
			if !v.IsSecret {
				val := v.Value
				vars = append(vars, protocol.DictionaryContextDataPair{
					Key: k, Value: protocol.PipelineContextData{
						Type:        &str,
						StringValue: &val,
					},
				})
			}
		}
		megascalerContextData = append(megascalerContextData, protocol.DictionaryContextDataPair{
			Key: "variables", Value: protocol.PipelineContextData{
				Type:            &dictionary,
				DictionaryValue: &vars,
			},
		})
	}
	rawjobreq["contextData"].(map[string]interface{})["github"].(map[string]interface{})["d"] = append(*jobreq.ContextData["github"].DictionaryValue, protocol.DictionaryContextDataPair{Key: "megascaler", Value: protocol.PipelineContextData{
		Type:            &dictionary,
		DictionaryValue: &megascalerContextData,
	}})

	src, err = json.Marshal(rawjobreq)
	if err != nil {
		return err
	}
	return we.ExecWorker(run, wc, jobreq, src)
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
	payload, err := github.ValidatePayload(r, []byte(s.Config.Secret))
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
					go s.remove(got)

					//got.cancelListener()
					//expected.cancelListener()
				} else {
					fmt.Println("job matched")
				}
			} else {
				fmt.Printf("job not found %v\n", name)
			}
			if expected != nil {
				if got != nil {
					expected.ActualJobRequest <- got.ExpectedJobRequest
				} else {
					expected.ActualJobRequest <- nil
					// unknown job with less labels hijacked the job
					go configureRunner(expected.ExpectedJobRequest.Payload, s, expected)
				}
			}
		} else {
			job := event.GetWorkflowJob()
			s.byWorkerName.Delete(job.GetRunnerName())
			if re, ok := s.byJobID.LoadAndDelete(job.GetID()); ok {
				e := re.(*workerData)
				s.remove(e)
			}
		}
	}
	w.WriteHeader(200)
}

func (s *GitHubEventMonitor) remove(got *workerData) {
	rr := &runnerconfiguration.RemoveRunner{}
	rr.Pat = s.Config.Pat
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
			func() {
				tc.Lock.Lock()
				defer tc.Lock.Unlock()
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
			}()
		} else if err != nil {
			fmt.Printf("failed to remove runner: %v\n", err)
		}
		if err == nil {
			s.byWorkerName.Delete(got.WorkerName)
		}
	} else {
		fmt.Println("can't remove runner, settings not set")
	}
}

func configureRunner(event *github.WorkflowJobEvent, s *GitHubEventMonitor, previousData *workerData) {
	var err error
	job := event.GetWorkflowJob()
	conf := &runnerconfiguration.ConfigureRunner{}
	conf.Unattended = true
	conf.Labels = job.Labels
	conf.Name = uuid.NewString()
	worker := s.Config.GetByLabels(conf.Labels)
	if worker == nil {
		return
	}
	fmt.Println("config runner")
	wd := &workerData{
		JobID:      job.GetID(),
		WorkerName: conf.Name,
		ExpectedJobRequest: &JobRequest{
			Payload:    event,
			WorkerArgs: worker.Args,
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
	conf.Pat = s.Config.Pat
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
		Data:   wd,
		Config: s.Config,
	}
	run := &actionsrunner.RunRunner{
		Settings: settings,
		Version:  "megascaler-v0.0.0",
	}
	go run.Run(we, listenerctx, context.Background())
}

type Worker struct {
	Labels []string `yaml:"labels"`
	Args   []string `yaml:"args"`
}

type Config struct {
	Worker      []*Worker `yaml:"worker"`
	Secret      string    `yaml:"secret"`
	Pat         string    `yaml:"pat"`
	MaxParallel int       `yaml:"max_parallel"`
	Address     string    `yaml:"address"`
}

func (config *Config) GetByLabels(labels []string) *Worker {
	for _, worker := range config.Worker {
		if equalFoldSet(labels, worker.Labels) {
			return worker
		}
	}
	return nil
}

func main() {
	src, _ := os.ReadFile("config.yml")
	conf := &Config{}
	yaml.Unmarshal(src, conf)
	http.ListenAndServe(conf.Address, &GitHubEventMonitor{
		Config: conf,
	})
}
