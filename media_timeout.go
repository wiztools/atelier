package main

// Shared timeout + abandonment policy for the queued media-generation backends
// (fal and Replicate). Both providers run jobs asynchronously: a submit returns
// immediately and the client polls, so a caller that stops polling does NOT
// stop the job — generation continues server-side and is billed on completion
// regardless of whether anyone fetches the result. The policy in
// runMediaGeneration exists to keep that server-side afterlife deliberate:
// a job that completed despite the timeout is fetched and delivered (the bill
// stands either way), and one still running is cancelled so it stops metering
// with nobody watching (conv_004f379a: a reframe sat 41 minutes in fal's queue
// on a downstream that had already died, then surfaced a 504).

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// defaultMediaGenerationTimeoutSeconds bounds one queued media generation when
// the config carries no value: generous enough for provider queue backlogs,
// tight enough that an unavailable downstream cannot hold a turn (and its
// billing meter) open indefinitely. Exposed in Settings → Others.
const defaultMediaGenerationTimeoutSeconds = 900

// mediaRecoveryTimeout bounds the post-timeout diagnosis: the status check,
// the cancel request, and — when the job completed in the meantime — the
// result download. Runs while the turn is still live, so it must stay short.
const mediaRecoveryTimeout = 90 * time.Second

// mediaGenerationTimeout resolves the configured per-call bound for queued
// media generations; a non-positive value falls back to the default.
func mediaGenerationTimeout(config AppConfig) time.Duration {
	if seconds := config.Generation.MediaTimeoutSeconds; seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return defaultMediaGenerationTimeoutSeconds * time.Second
}

// mediaJob is the provider-agnostic handle for one queued media generation —
// fal's request id or Replicate's prediction id plus the routes that drive it.
// The queue lifecycle writes it through the client's jobSink the moment the
// provider accepts a submit; runMediaGeneration uses it to diagnose and stop
// the job when the local timeout fires. The JSON shape is what a future
// handle-persistence layer would store on the tool activity.
type mediaJob struct {
	Provider  string `json:"provider"`
	ID        string `json:"id"`
	StatusURL string `json:"statusUrl,omitempty"`
	ResultURL string `json:"resultUrl,omitempty"`
	CancelURL string `json:"cancelUrl,omitempty"`
}

// record is the sink the queue lifecycles write through (the clients hold a
// *mediaJob and publish their handle right after a successful submit).
func (job *mediaJob) record(handle mediaJob) {
	if job != nil {
		*job = handle
	}
}

// mediaJobState is the provider-normalized classification of one queued job.
type mediaJobState int

const (
	mediaJobUnknown mediaJobState = iota
	mediaJobRunning
	mediaJobCompleted
	mediaJobFailed
	mediaJobCanceled
)

// mediaGenerationRecoveredNotice lands on a delivered result whose generation
// outlived the local timeout but completed anyway — recovered rather than
// discarded, because the provider billed it either way.
const mediaGenerationRecoveredNotice = "The provider finished the generation after the local timeout — the completed result was recovered."

// runMediaGeneration bounds one queued media generation by timeout and applies
// the shared abandonment policy when it fires. run receives the timeout
// context and reports the submitted job's handle through job; cancelJob stops
// a still-running job; recoverJob (queue backends only) diagnoses the job
// after the timeout — delivering a completed result, or cancelling one that is
// still running and reporting why. The bool reports that the result was
// recovered after the timeout, so call sites can notice it.
//
// Errors that do not unwrap to a context error pass through untouched:
// deterministic provider failures (auth, validation, 4xx) must never be
// reinterpreted as timeouts. When the turn itself ends first (user abort,
// stream close) the job is cancelled best-effort on a detached context — an
// abandoned generation should stop billing — and the original error stands.
func runMediaGeneration[T any](
	ctx context.Context,
	timeout time.Duration,
	job *mediaJob,
	run func(context.Context) (T, error),
	cancelJob func(context.Context, mediaJob) error,
	recoverJob func(context.Context, mediaJob) (T, error),
) (T, bool, error) {
	var zero T
	if timeout <= 0 {
		result, err := run(ctx)
		return result, false, err
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := run(tctx)
	if err == nil {
		return result, false, nil
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return zero, false, err
	}
	handle := mediaJob{}
	if job != nil {
		handle = *job
	}
	if ctx.Err() != nil {
		if handle.ID != "" && cancelJob != nil {
			_ = cancelJob(context.WithoutCancel(ctx), handle)
		}
		return zero, false, err
	}
	if handle.ID == "" {
		return zero, false, fmt.Errorf("media generation timed out after %s, before the provider accepted the job", timeout)
	}
	rctx, rcancel := context.WithTimeout(ctx, mediaRecoveryTimeout)
	defer rcancel()
	if recoverJob != nil {
		recovered, rerr := recoverJob(rctx, handle)
		if rerr == nil {
			return recovered, true, nil
		}
		if !errors.Is(rerr, context.DeadlineExceeded) && !errors.Is(rerr, context.Canceled) {
			return zero, false, fmt.Errorf("media generation timed out after %s: %w", timeout, rerr)
		}
	}
	cancelNote := "the job was cancelled"
	if cancelJob != nil {
		if cerr := cancelJob(rctx, handle); cerr != nil {
			cancelNote = fmt.Sprintf("the job could not be cancelled (%s) and may still be running", cerr)
		}
	}
	return zero, false, fmt.Errorf("media generation timed out after %s: %s (%s job %s)", timeout, cancelNote, handle.Provider, handle.ID)
}
