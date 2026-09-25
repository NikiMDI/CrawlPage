package crawler

import "context"

type crawlTask struct {
	Job      crawlJob
	Sequence int
}

type crawlWorkerResult struct {
	Task         crawlTask
	Page         PageResult
	PageAccepted bool
	Err          error
}

func (i *Inspector) runWorker(
	ctx context.Context,
	session *crawlSession,
	jobs <-chan crawlTask,
	results chan<- crawlWorkerResult,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case task, open := <-jobs:
			if !open {
				return
			}
			if ctx.Err() != nil {
				return
			}

			page, pageAccepted, err := i.inspectURL(
				ctx,
				task.Job.URL,
				task.Job.Depth,
				session,
			)
			workerResult := crawlWorkerResult{
				Task:         task,
				Page:         page,
				PageAccepted: pageAccepted,
				Err:          err,
			}

			select {
			case results <- workerResult:
			case <-ctx.Done():
				return
			}
		}
	}
}
