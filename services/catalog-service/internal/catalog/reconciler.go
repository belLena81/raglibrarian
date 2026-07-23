package catalog

import (
	"context"
	"errors"
	"regexp"
	"time"
)

const (
	reconcilePrefix         = "originals/"
	reconcileBatchSize      = 100
	maximumReconcileObjects = 1000
)

var generatedObjectReference = regexp.MustCompile(`^originals/[A-Za-z0-9_-]{22}\.(pdf|epub)$`)

type StoredObject struct {
	Reference    string
	Size         int64
	LastModified time.Time
}

type ReconciliationRepository interface {
	ReferencesExist(context.Context, []string) (map[string]bool, error)
}

type PendingOriginalDeletion struct {
	BookID           string
	CommandID        string
	LifecycleVersion int64
	ObjectReference  string
}

type DeletionReconciliationRepository interface {
	PendingOriginalDeletions(context.Context, int) ([]PendingOriginalDeletion, error)
	MarkOriginalDeleted(context.Context, string, string, int64, time.Time) (Book, error)
}

type ReconciliationObjectStore interface {
	ListCompleted(context.Context, string, string, int) ([]StoredObject, string, error)
	Delete(context.Context, string) error
}

type ReconciliationRecorder interface {
	ReconciliationCompleted(scanned, deleted int)
	ReconciliationFailed()
}

type ReconciliationResult struct {
	Scanned    int
	Deleted    int
	NextCursor string
}

type Reconciler struct {
	repository  ReconciliationRepository
	objects     ReconciliationObjectStore
	gracePeriod time.Duration
	recorder    ReconciliationRecorder
	now         func() time.Time
}

func NewReconciler(repository ReconciliationRepository, objects ReconciliationObjectStore, gracePeriod time.Duration, recorder ReconciliationRecorder) *Reconciler {
	if repository == nil || objects == nil || recorder == nil {
		panic("catalog reconciler dependencies are required")
	}
	if gracePeriod <= 0 {
		panic("catalog reconciler grace period is required")
	}
	return &Reconciler{repository: repository, objects: objects, gracePeriod: gracePeriod, recorder: recorder, now: func() time.Time { return time.Now().UTC() }}
}

func (r *Reconciler) RunPass(ctx context.Context, cursor string) (ReconciliationResult, error) {
	result := ReconciliationResult{NextCursor: cursor}
	if repository, ok := r.repository.(DeletionReconciliationRepository); ok {
		pending, err := repository.PendingOriginalDeletions(ctx, reconcileBatchSize)
		if err != nil {
			r.recorder.ReconciliationFailed()
			return result, errors.New("catalog deletion reconciliation lookup failed")
		}
		for _, deletion := range pending {
			if err = r.objects.Delete(ctx, deletion.ObjectReference); err != nil {
				r.recorder.ReconciliationFailed()
				return result, errors.New("catalog deletion reconciliation object deletion failed")
			}
			if _, err = repository.MarkOriginalDeleted(
				ctx,
				deletion.BookID,
				deletion.CommandID,
				deletion.LifecycleVersion,
				r.now().UTC(),
			); err != nil {
				r.recorder.ReconciliationFailed()
				return result, errors.New("catalog deletion reconciliation persistence failed")
			}
			result.Deleted++
		}
	}
	candidates := make([]string, 0, maximumReconcileObjects)
	cutoff := r.now().Add(-r.gracePeriod)
	for result.Scanned < maximumReconcileObjects {
		limit := min(reconcileBatchSize, maximumReconcileObjects-result.Scanned)
		objects, next, err := r.objects.ListCompleted(ctx, reconcilePrefix, result.NextCursor, limit)
		if err != nil {
			r.recorder.ReconciliationFailed()
			return result, errors.New("catalog reconciliation storage listing failed")
		}
		for _, object := range objects {
			result.Scanned++
			if generatedObjectReference.MatchString(object.Reference) && object.LastModified.Before(cutoff) {
				candidates = append(candidates, object.Reference)
			}
		}
		result.NextCursor = next
		if next == "" || len(objects) == 0 {
			break
		}
	}

	orphans := make([]string, 0, len(candidates))
	for start := 0; start < len(candidates); start += reconcileBatchSize {
		end := min(start+reconcileBatchSize, len(candidates))
		exists, err := r.repository.ReferencesExist(ctx, candidates[start:end])
		if err != nil {
			r.recorder.ReconciliationFailed()
			return result, errors.New("catalog reconciliation reference lookup failed")
		}
		for _, reference := range candidates[start:end] {
			if !exists[reference] {
				orphans = append(orphans, reference)
			}
		}
	}
	for _, reference := range orphans {
		if err := r.objects.Delete(ctx, reference); err != nil {
			r.recorder.ReconciliationFailed()
			return result, errors.New("catalog reconciliation object deletion failed")
		}
		result.Deleted++
	}
	r.recorder.ReconciliationCompleted(result.Scanned, result.Deleted)
	return result, nil
}

func RunReconciliation(ctx context.Context, reconciler *Reconciler, interval time.Duration) {
	if reconciler == nil || interval <= 0 {
		panic("catalog reconciliation runner configuration is required")
	}
	cursor := ""
	for {
		result, _ := reconciler.RunPass(ctx, cursor)
		cursor = result.NextCursor
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
