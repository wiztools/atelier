package main

// Tests for the shared media-generation timeout policy (media_timeout.go) and
// the per-provider queue lifecycle primitives it drives. HTTP is mocked with
// the roundTripFunc pattern; the policy tests are pure closure tests.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRunMediaGenerationReturnsResultWithoutPolicy(t *testing.T) {
	cancelled := false
	result, recovered, err := runMediaGeneration(context.Background(), 10*time.Second, &mediaJob{},
		func(context.Context) (string, error) { return "clip", nil },
		func(context.Context, mediaJob) error { cancelled = true; return nil },
		nil,
	)
	if err != nil || result != "clip" || recovered {
		t.Fatalf("expected clean result, got result=%q recovered=%v err=%v", result, recovered, err)
	}
	if cancelled {
		t.Fatal("cancel must not run on success")
	}
}

func TestRunMediaGenerationPassesProviderErrorsThrough(t *testing.T) {
	providerErr := errors.New("fal authentication failed")
	cancelled := false
	_, _, err := runMediaGeneration(context.Background(), 10*time.Second, &mediaJob{},
		func(context.Context) (string, error) { return "", providerErr },
		func(context.Context, mediaJob) error { cancelled = true; return nil },
		func(context.Context, mediaJob) (string, error) { return "recovered", nil },
	)
	if !errors.Is(err, providerErr) {
		t.Fatalf("expected provider error untouched, got %v", err)
	}
	if cancelled {
		t.Fatal("deterministic provider errors must not trigger cancellation")
	}
}

func TestRunMediaGenerationRecoversCompletedJob(t *testing.T) {
	recovered := false
	job := &mediaJob{}
	result, gotRecovered, err := runMediaGeneration(context.Background(), 25*time.Millisecond, job,
		func(ctx context.Context) (string, error) {
			job.record(mediaJob{Provider: "fal", ID: "req-1"})
			<-ctx.Done()
			return "", ctx.Err()
		},
		func(context.Context, mediaJob) error {
			t.Error("cancel must not run when the job completed and was recovered")
			return nil
		},
		func(_ context.Context, job mediaJob) (string, error) {
			if job.ID != "req-1" {
				t.Errorf("recovery received job %q", job.ID)
			}
			recovered = true
			return "clip", nil
		},
	)
	if err != nil || result != "clip" || !gotRecovered || !recovered {
		t.Fatalf("expected recovered result, got result=%q recovered=%v err=%v", result, gotRecovered, err)
	}
}

func TestRunMediaGenerationCancelsRunningJob(t *testing.T) {
	cancelled := false
	job := &mediaJob{}
	_, _, err := runMediaGeneration(context.Background(), 25*time.Millisecond, job,
		func(ctx context.Context) (string, error) {
			job.record(mediaJob{Provider: "fal", ID: "req-2"})
			<-ctx.Done()
			return "", ctx.Err()
		},
		func(_ context.Context, job mediaJob) error {
			cancelled = true
			if job.ID != "req-2" {
				t.Errorf("cancel received job %q", job.ID)
			}
			return nil
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !cancelled {
		t.Fatal("expected the running job to be cancelled")
	}
	if !strings.Contains(err.Error(), "req-2") || !strings.Contains(err.Error(), "was cancelled") {
		t.Fatalf("timeout error should name the job and its cancel outcome, got: %v", err)
	}
}

func TestRunMediaGenerationReportsCancelFailure(t *testing.T) {
	job := &mediaJob{}
	_, _, err := runMediaGeneration(context.Background(), 25*time.Millisecond, job,
		func(ctx context.Context) (string, error) {
			job.record(mediaJob{Provider: "fal", ID: "req-3"})
			<-ctx.Done()
			return "", ctx.Err()
		},
		func(context.Context, mediaJob) error { return errors.New("503 service unavailable") },
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "could not be cancelled") {
		t.Fatalf("expected cancel-failure notice in error, got: %v", err)
	}
}

func TestRunMediaGenerationParentCancelStopsJobAndKeepsError(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	cancelled := make(chan struct{}, 1)
	job := &mediaJob{}
	_, _, err := runMediaGeneration(parent, 10*time.Second, job,
		func(ctx context.Context) (string, error) {
			job.record(mediaJob{Provider: "fal", ID: "req-4"})
			<-ctx.Done()
			return "", ctx.Err()
		},
		func(context.Context, mediaJob) error { cancelled <- struct{}{}; return nil },
		nil,
	)
	if err == nil {
		t.Fatal("expected the original cancellation error")
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("an abandoned turn must still cancel the remote job")
	}
}

func TestRunMediaGenerationTimeoutBeforeSubmit(t *testing.T) {
	_, _, err := runMediaGeneration(context.Background(), 25*time.Millisecond, &mediaJob{},
		func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		func(context.Context, mediaJob) error { t.Error("nothing was submitted"); return nil },
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "before the provider accepted the job") {
		t.Fatalf("expected no-handle timeout error, got: %v", err)
	}
}

func TestMediaGenerationTimeoutConfig(t *testing.T) {
	if got := mediaGenerationTimeout(AppConfig{}); got != defaultMediaGenerationTimeoutSeconds*time.Second {
		t.Fatalf("empty config should resolve the default, got %s", got)
	}
	if got := mediaGenerationTimeout(AppConfig{Generation: ConfigGeneration{MediaTimeoutSeconds: 60}}); got != time.Minute {
		t.Fatalf("configured seconds should resolve, got %s", got)
	}
	if got := mediaGenerationTimeout(AppConfig{Generation: ConfigGeneration{MediaTimeoutSeconds: -5}}); got != defaultMediaGenerationTimeoutSeconds*time.Second {
		t.Fatalf("junk values fall back to the default, got %s", got)
	}
}

func TestFalClientJobStatus(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantState mediaJobState
		wantText  string
	}{
		{"completed", `{"status":"COMPLETED"}`, mediaJobCompleted, ""},
		{"failed", `{"status":"FAILED","error":"downstream exploded"}`, mediaJobFailed, "downstream exploded"},
		{"in queue", `{"status":"IN_QUEUE"}`, mediaJobRunning, ""},
		{"in progress", `{"status":"IN_PROGRESS"}`, mediaJobRunning, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/status") {
					t.Errorf("unexpected status call %s %s", req.Method, req.URL.Path)
				}
				return jsonResp(tc.body), nil
			}))
			state, detail, err := client.jobStatus(context.Background(), mediaJob{StatusURL: falQueueBaseURL + "/fal-ai/x/requests/r1/status"})
			if err != nil {
				t.Fatalf("jobStatus returned %v", err)
			}
			if state != tc.wantState || detail != tc.wantText {
				t.Fatalf("got state=%d detail=%q, want state=%d detail=%q", state, detail, tc.wantState, tc.wantText)
			}
		})
	}
}

func TestFalClientCancelMediaJob(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"accepted", http.StatusAccepted, `{"status":"CANCELLATION_REQUESTED"}`, false},
		{"already completed", http.StatusBadRequest, `{"status":"ALREADY_COMPLETED"}`, false},
		{"not found", http.StatusNotFound, `{"status":"NOT_FOUND"}`, false},
		{"server error", http.StatusInternalServerError, `{}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPut || !strings.HasSuffix(req.URL.Path, "/cancel") {
					t.Errorf("unexpected cancel call %s %s", req.Method, req.URL.Path)
				}
				return &http.Response{
					StatusCode: tc.status,
					Status:     fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)),
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Header:     http.Header{},
				}, nil
			}))
			err := client.CancelMediaJob(context.Background(), mediaJob{CancelURL: falQueueBaseURL + "/fal-ai/x/requests/r1/cancel"})
			if tc.wantErr != (err != nil) {
				t.Fatalf("CancelMediaJob err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestFalClientRecoverVideoJobFetchesCompletedResult(t *testing.T) {
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case strings.HasSuffix(req.URL.Path, "/requests/r1"):
			return jsonResp(`{"video":{"url":"https://cdn.fal.dev/out.mp4"}}`), nil
		case req.URL.Host == "cdn.fal.dev":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(string(tinyMP4()))),
				Header:     http.Header{"Content-Type": []string{"video/mp4"}},
			}, nil
		default:
			t.Errorf("unexpected call %s %s", req.Method, req.URL)
			return jsonResp(`{}`), nil
		}
	}))
	generated, err := client.RecoverVideoJob(context.Background(), mediaJob{
		ID:        "r1",
		StatusURL: falQueueBaseURL + "/fal-ai/x/requests/r1/status",
		ResultURL: falQueueBaseURL + "/fal-ai/x/requests/r1",
		CancelURL: falQueueBaseURL + "/fal-ai/x/requests/r1/cancel",
	})
	if err != nil {
		t.Fatalf("RecoverVideoJob returned %v", err)
	}
	if len(generated.Data) == 0 || generated.MimeType != "video/mp4" {
		t.Fatalf("expected a recovered clip, got %+v", generated)
	}
}

func TestFalClientRecoverVideoJobCancelsStillRunning(t *testing.T) {
	statusCalls := 0
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/status"):
			statusCalls++
			return jsonResp(`{"status":"IN_QUEUE"}`), nil
		case strings.HasSuffix(req.URL.Path, "/cancel"):
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Status:     "202 Accepted",
				Body:       io.NopCloser(strings.NewReader(`{"status":"CANCELLATION_REQUESTED"}`)),
				Header:     http.Header{},
			}, nil
		default:
			t.Errorf("unexpected call %s %s", req.Method, req.URL)
			return jsonResp(`{}`), nil
		}
	}))
	_, err := client.RecoverVideoJob(context.Background(), mediaJob{
		ID:        "r9",
		StatusURL: falQueueBaseURL + "/fal-ai/x/requests/r9/status",
		ResultURL: falQueueBaseURL + "/fal-ai/x/requests/r9",
		CancelURL: falQueueBaseURL + "/fal-ai/x/requests/r9/cancel",
	})
	if err == nil || !strings.Contains(err.Error(), "r9 was cancelled after the timeout") {
		t.Fatalf("expected still-running outcome, got: %v", err)
	}
	if statusCalls != 2 {
		t.Fatalf("expected status check before and after cancel, got %d", statusCalls)
	}
}

func TestReplicateClientCancelPrediction(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"accepted", http.StatusOK, false},
		{"already terminated", http.StatusConflict, false},
		{"server error", http.StatusInternalServerError, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/cancel") {
					t.Errorf("unexpected cancel call %s %s", req.Method, req.URL.Path)
				}
				return &http.Response{
					StatusCode: tc.status,
					Status:     fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)),
					Body:       io.NopCloser(strings.NewReader(`{}`)),
					Header:     http.Header{},
				}, nil
			}))
			err := client.CancelPrediction(context.Background(), "pred-1")
			if tc.wantErr != (err != nil) {
				t.Fatalf("CancelPrediction err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestReplicateClientRecoverVideoJobFetchesCompletedResult(t *testing.T) {
	fetches := 0
	client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/predictions/pred-1"):
			fetches++
			return jsonResp(`{"id":"pred-1","status":"succeeded","output":"https://cdn.replicate.dev/out.mp4"}`), nil
		case req.URL.Host == "cdn.replicate.dev":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(string(tinyMP4()))),
				Header:     http.Header{"Content-Type": []string{"video/mp4"}},
			}, nil
		default:
			t.Errorf("unexpected call %s %s", req.Method, req.URL)
			return jsonResp(`{}`), nil
		}
	}))
	generated, err := client.RecoverVideoJob(context.Background(), mediaJob{Provider: "replicate", ID: "pred-1"})
	if err != nil {
		t.Fatalf("RecoverVideoJob returned %v", err)
	}
	if len(generated.Data) == 0 || fetches != 1 {
		t.Fatalf("expected one prediction fetch and a recovered clip, got fetches=%d data=%d", fetches, len(generated.Data))
	}
}

func TestReplicateClientRecoverImageJobReportsFailure(t *testing.T) {
	client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResp(`{"id":"pred-2","status":"failed","error":"NSFW content detected"}`), nil
	}))
	_, err := client.RecoverImageJob(context.Background(), mediaJob{Provider: "replicate", ID: "pred-2"}, "owner/model")
	if err == nil || !strings.Contains(err.Error(), "NSFW content detected") {
		t.Fatalf("expected failed-prediction detail, got: %v", err)
	}
}

func TestReplicateClientRecoverImageJobFetchesCompletedResult(t *testing.T) {
	client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/predictions/pred-3"):
			return jsonResp(`{"id":"pred-3","status":"succeeded","output":["https://cdn.replicate.dev/out.png"]}`), nil
		case req.URL.Host == "cdn.replicate.dev":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(string(mustDecodeTinyPNG()))),
				Header:     http.Header{"Content-Type": []string{"image/png"}},
			}, nil
		default:
			t.Errorf("unexpected call %s %s", req.Method, req.URL)
			return jsonResp(`{}`), nil
		}
	}))
	resp, err := client.RecoverImageJob(context.Background(), mediaJob{Provider: "replicate", ID: "pred-3"}, "owner/model")
	if err != nil {
		t.Fatalf("RecoverImageJob returned %v", err)
	}
	if resp.Model != "owner/model" || !strings.HasPrefix(resp.Image, "data:image/png;base64,") {
		t.Fatalf("expected a recovered labeled image, got model=%q image-prefix-ok=%v", resp.Model, strings.HasPrefix(resp.Image, "data:image/png;base64,"))
	}
}
