package git

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/sourcegraph/log"
	"go.opentelemetry.io/otel/attribute"

	"github.com/sourcegraph/sourcegraph/internal/api"
	"github.com/sourcegraph/sourcegraph/internal/gitserver/gitdomain"
	"github.com/sourcegraph/sourcegraph/internal/metrics"
	"github.com/sourcegraph/sourcegraph/internal/observation"
	"github.com/sourcegraph/sourcegraph/lib/errors"
)

var (
	concurrentOps = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "src_gitserver_backend_concurrent_operations",
		Help: "Concurrent operations handled by Gitserver backends.",
	}, []string{"op"})
)

func NewObservableBackend(backend GitBackend) GitBackend {
	return &observableBackend{
		backend:    backend,
		operations: getOperations(),
	}
}

type observableBackend struct {
	operations *operations
	backend    GitBackend
}

func (b *observableBackend) Config() GitConfigBackend {
	return &observableGitConfigBackend{
		backend:    b.backend.Config(),
		operations: b.operations,
	}
}

type observableGitConfigBackend struct {
	operations *operations
	backend    GitConfigBackend
}

func (b *observableGitConfigBackend) Get(ctx context.Context, key string) (_ string, err error) {
	ctx, _, endObservation := b.operations.configGet.With(ctx, &err, observation.Args{})
	defer endObservation(1, observation.Args{})

	concurrentOps.WithLabelValues("Config.Get").Inc()
	defer concurrentOps.WithLabelValues("Config.Get").Dec()

	return b.backend.Get(ctx, key)
}

func (b *observableGitConfigBackend) Set(ctx context.Context, key, value string) (err error) {
	ctx, _, endObservation := b.operations.configSet.With(ctx, &err, observation.Args{})
	defer endObservation(1, observation.Args{})

	concurrentOps.WithLabelValues("Config.Set").Inc()
	defer concurrentOps.WithLabelValues("Config.Set").Dec()

	return b.backend.Set(ctx, key, value)
}

func (b *observableGitConfigBackend) Unset(ctx context.Context, key string) (err error) {
	ctx, _, endObservation := b.operations.configUnset.With(ctx, &err, observation.Args{})
	defer endObservation(1, observation.Args{})

	concurrentOps.WithLabelValues("Config.Unset").Inc()
	defer concurrentOps.WithLabelValues("Config.Unset").Dec()

	return b.backend.Unset(ctx, key)
}

func (b *observableBackend) GetObject(ctx context.Context, objectName string) (_ *gitdomain.GitObject, err error) {
	ctx, _, endObservation := b.operations.getObject.With(ctx, &err, observation.Args{})
	defer endObservation(1, observation.Args{})

	concurrentOps.WithLabelValues("GetObject").Inc()
	defer concurrentOps.WithLabelValues("GetObject").Dec()

	return b.backend.GetObject(ctx, objectName)
}

func (b *observableBackend) MergeBase(ctx context.Context, baseRevspec, headRevspec string) (_ api.CommitID, err error) {
	ctx, _, endObservation := b.operations.mergeBase.With(ctx, &err, observation.Args{})
	defer endObservation(1, observation.Args{})

	concurrentOps.WithLabelValues("MergeBase").Inc()
	defer concurrentOps.WithLabelValues("MergeBase").Dec()

	return b.backend.MergeBase(ctx, baseRevspec, headRevspec)
}

func (b *observableBackend) Blame(ctx context.Context, commit api.CommitID, path string, opt BlameOptions) (_ BlameHunkReader, err error) {
	ctx, errCollector, endObservation := b.operations.blame.WithErrors(ctx, &err, observation.Args{})
	ctx, cancel := context.WithCancel(ctx)
	endObservation.OnCancel(ctx, 1, observation.Args{})

	concurrentOps.WithLabelValues("Blame").Inc()

	hr, err := b.backend.Blame(ctx, commit, path, opt)
	if err != nil {
		concurrentOps.WithLabelValues("Blame").Dec()
		cancel()
		return nil, err
	}

	return &observableBlameHunkReader{
		inner: hr,
		onClose: func(err error) {
			concurrentOps.WithLabelValues("Blame").Dec()
			errCollector.Collect(&err)
			cancel()
		},
	}, nil
}

type observableBlameHunkReader struct {
	inner   BlameHunkReader
	onClose func(err error)
}

func (hr *observableBlameHunkReader) Read() (*gitdomain.Hunk, error) {
	return hr.inner.Read()
}

func (hr *observableBlameHunkReader) Close() error {
	err := hr.inner.Close()
	hr.onClose(err)
	return err
}

func (b *observableBackend) SymbolicRefHead(ctx context.Context, short bool) (_ string, err error) {
	ctx, _, endObservation := b.operations.symbolicRefHead.With(ctx, &err, observation.Args{})
	defer endObservation(1, observation.Args{})

	concurrentOps.WithLabelValues("SymbolicRefHead").Inc()
	defer concurrentOps.WithLabelValues("SymbolicRefHead").Dec()

	return b.backend.SymbolicRefHead(ctx, short)
}

func (b *observableBackend) RevParseHead(ctx context.Context) (_ api.CommitID, err error) {
	ctx, _, endObservation := b.operations.revParseHead.With(ctx, &err, observation.Args{})
	defer endObservation(1, observation.Args{})

	concurrentOps.WithLabelValues("RevParseHead").Inc()
	defer concurrentOps.WithLabelValues("RevParseHead").Dec()

	return b.backend.RevParseHead(ctx)
}

func (b *observableBackend) GetCommit(ctx context.Context, commit api.CommitID, includeModifiedFiles bool) (_ *GitCommitWithFiles, err error) {
	ctx, _, endObservation := b.operations.getCommit.With(ctx, &err, observation.Args{
		Attrs: []attribute.KeyValue{
			attribute.String("commit", string(commit)),
			attribute.Bool("includeModifiedFiles", includeModifiedFiles),
		},
	})
	defer endObservation(1, observation.Args{})

	concurrentOps.WithLabelValues("GetCommit").Inc()
	defer concurrentOps.WithLabelValues("GetCommit").Dec()

	return b.backend.GetCommit(ctx, commit, includeModifiedFiles)
}

func (b *observableBackend) ReadFile(ctx context.Context, commit api.CommitID, path string) (_ io.ReadCloser, err error) {
	ctx, errCollector, endObservation := b.operations.readFile.WithErrors(ctx, &err, observation.Args{})
	ctx, cancel := context.WithCancel(ctx)
	endObservation.OnCancel(ctx, 1, observation.Args{})

	concurrentOps.WithLabelValues("ReadFile").Inc()

	r, err := b.backend.ReadFile(ctx, commit, path)
	if err != nil {
		concurrentOps.WithLabelValues("ReadFile").Dec()
		cancel()
		return nil, err
	}

	return &observableReadCloser{
		inner: r,
		endObservation: func(err error) {
			concurrentOps.WithLabelValues("ReadFile").Dec()
			errCollector.Collect(&err)
			cancel()
		},
	}, nil
}

func (b *observableBackend) Exec(ctx context.Context, args ...string) (_ io.ReadCloser, err error) {
	ctx, errCollector, endObservation := b.operations.exec.WithErrors(ctx, &err, observation.Args{})
	ctx, cancel := context.WithCancel(ctx)
	endObservation.OnCancel(ctx, 1, observation.Args{})

	concurrentOps.WithLabelValues("Exec").Inc()

	r, err := b.backend.Exec(ctx, args...)
	if err != nil {
		concurrentOps.WithLabelValues("Exec").Dec()
		cancel()
		return nil, err
	}

	return &observableReadCloser{
		inner: r,
		endObservation: func(err error) {
			concurrentOps.WithLabelValues("Exec").Dec()
			errCollector.Collect(&err)
			cancel()
		},
	}, nil
}

func (b *observableBackend) ArchiveReader(ctx context.Context, format ArchiveFormat, treeish string, paths []string) (_ io.ReadCloser, err error) {
	ctx, errCollector, endObservation := b.operations.archiveReader.WithErrors(ctx, &err, observation.Args{})
	ctx, cancel := context.WithCancel(ctx)
	endObservation.OnCancel(ctx, 1, observation.Args{})

	concurrentOps.WithLabelValues("ArchiveReader").Inc()

	r, err := b.backend.ArchiveReader(ctx, format, treeish, paths)
	if err != nil {
		concurrentOps.WithLabelValues("ArchiveReader").Dec()
		cancel()
		return nil, err
	}

	return &observableReadCloser{
		inner: r,
		endObservation: func(err error) {
			concurrentOps.WithLabelValues("ArchiveReader").Dec()
			errCollector.Collect(&err)
			cancel()
		},
	}, nil
}

func (b *observableBackend) ResolveRevision(ctx context.Context, revspec string) (_ api.CommitID, err error) {
	ctx, _, endObservation := b.operations.resolveRevision.With(ctx, &err, observation.Args{
		Attrs: []attribute.KeyValue{
			attribute.String("revspec", revspec),
		},
	})
	defer endObservation(1, observation.Args{})

	concurrentOps.WithLabelValues("ResolveRevision").Inc()
	defer concurrentOps.WithLabelValues("ResolveRevision").Dec()

	return b.backend.ResolveRevision(ctx, revspec)
}

type observableReadCloser struct {
	inner          io.ReadCloser
	endObservation func(err error)
}

func (r *observableReadCloser) Read(p []byte) (int, error) {
	return r.inner.Read(p)
}

func (r *observableReadCloser) Close() error {
	err := r.inner.Close()
	r.endObservation(err)
	return err
}

type operations struct {
	configGet       *observation.Operation
	configSet       *observation.Operation
	configUnset     *observation.Operation
	getObject       *observation.Operation
	mergeBase       *observation.Operation
	blame           *observation.Operation
	symbolicRefHead *observation.Operation
	revParseHead    *observation.Operation
	readFile        *observation.Operation
	exec            *observation.Operation
	getCommit       *observation.Operation
	archiveReader   *observation.Operation
	resolveRevision *observation.Operation
}

func newOperations(observationCtx *observation.Context) *operations {
	redMetrics := metrics.NewREDMetrics(
		observationCtx.Registerer,
		"gitserver_backend",
		metrics.WithLabels("op"),
		metrics.WithCountHelp("Total number of method invocations."),
	)

	op := func(name string) *observation.Operation {
		return observationCtx.Operation(observation.Op{
			Name:              fmt.Sprintf("gitserver.backend.%s", name),
			MetricLabelValues: []string{name},
			Metrics:           redMetrics,
			ErrorFilter: func(err error) observation.ErrorFilterBehaviour {
				if errors.HasType(err, &gitdomain.RevisionNotFoundError{}) {
					return observation.EmitForLogs
				}
				return observation.EmitForAllExceptLogs
			},
		})
	}

	return &operations{
		configGet:       op("config-get"),
		configSet:       op("config-set"),
		configUnset:     op("config-unset"),
		getObject:       op("get-object"),
		mergeBase:       op("merge-base"),
		blame:           op("blame"),
		symbolicRefHead: op("symbolic-ref-head"),
		revParseHead:    op("rev-parse-head"),
		readFile:        op("read-file"),
		exec:            op("exec"),
		getCommit:       op("get-commit"),
		archiveReader:   op("archive-reader"),
		resolveRevision: op("resolve-revision"),
	}
}

var (
	operationsInst     *operations
	operationsInstOnce sync.Once
)

func getOperations() *operations {
	operationsInstOnce.Do(func() {
		observationCtx := observation.NewContext(log.Scoped("gitserver.backend"))
		operationsInst = newOperations(observationCtx)
	})

	return operationsInst
}
