package parser

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
)

var (
	// ErrBatchProcessorStopped indicates the batch processor has been stopped
	ErrBatchProcessorStopped = errors.New("batch processor stopped")
	// ErrBatchTimeout indicates a batch processing timeout
	ErrBatchTimeout = errors.New("batch processing timeout")
)

// BatchProcessor handles batch processing of SQL statements
type BatchProcessor struct {
	batchSize    int
	workers      int
	queue        chan *Statement
	timeout      time.Duration
	memOptimizer *MemoryOptimizer
	errorHandler func(error)
	workerPool   chan struct{}
	mu           sync.RWMutex
	stopped      bool
}

// BatchConfig represents batch processor configuration
type BatchConfig struct {
	BatchSize    int
	Workers      int
	Timeout      time.Duration
	MemOptimizer *MemoryOptimizer
	ErrorHandler func(error)
}

// NewBatchProcessor creates a new batch processor
func NewBatchProcessor(config BatchConfig) *BatchProcessor {
	if config.Workers == 0 {
		config.Workers = runtime.NumCPU()
	}
	if config.BatchSize == 0 {
		config.BatchSize = 1000
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.ErrorHandler == nil {
		config.ErrorHandler = func(err error) {
			// Default error handler just ignores the error
		}
	}

	return &BatchProcessor{
		batchSize:    config.BatchSize,
		workers:      config.Workers,
		queue:        make(chan *Statement, config.BatchSize),
		timeout:      config.Timeout,
		memOptimizer: config.MemOptimizer,
		errorHandler: config.ErrorHandler,
		workerPool:   make(chan struct{}, config.Workers),
	}
}

// ProcessBatch processes statements in batches
func (bp *BatchProcessor) ProcessBatch(ctx context.Context, stmts []*Statement, handler func(*Statement) error) error {
	if bp.isStopped() {
		return ErrBatchProcessorStopped
	}

	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, bp.timeout)
	defer cancel()

	var wg sync.WaitGroup
	errChan := make(chan error, bp.workers)

	// Start worker pool
	for i := 0; i < bp.workers; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case stmt, ok := <-bp.queue:
					if !ok {
						return
					}
					if err := bp.processStatement(ctx, stmt, handler); err != nil {
						select {
						case errChan <- err:
						default:
							bp.errorHandler(err)
						}
						return
					}
				}
			}
		}()
	}

	// Send statements to workers
	for _, stmt := range stmts {
		select {
		case <-ctx.Done():
			close(bp.queue)
			return ctx.Err()
		case bp.queue <- stmt:
		case err := <-errChan:
			close(bp.queue)
			return err
		}
	}

	close(bp.queue)
	wg.Wait()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// processStatement processes a single statement with memory optimization
func (bp *BatchProcessor) processStatement(ctx context.Context, stmt *Statement, handler func(*Statement) error) error {
	// Acquire worker from pool
	select {
	case bp.workerPool <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-bp.workerPool }()

	// Process with memory optimization if available
	if bp.memOptimizer != nil {
		buf := bp.memOptimizer.GetBuffer()
		defer bp.memOptimizer.PutBuffer(buf)
	}

	return handler(stmt)
}

// Stop stops the batch processor
func (bp *BatchProcessor) Stop() {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if !bp.stopped {
		bp.stopped = true
		close(bp.queue)
	}
}

// isStopped checks if the processor is stopped
func (bp *BatchProcessor) isStopped() bool {
	bp.mu.RLock()
	defer bp.mu.RUnlock()
	return bp.stopped
}

// SetTimeout sets the processing timeout
func (bp *BatchProcessor) SetTimeout(timeout time.Duration) {
	bp.mu.Lock()
	bp.timeout = timeout
	bp.mu.Unlock()
}

// SetBatchSize sets the batch size
func (bp *BatchProcessor) SetBatchSize(size int) {
	bp.mu.Lock()
	bp.batchSize = size
	bp.mu.Unlock()
}

// SetErrorHandler sets the error handler function
func (bp *BatchProcessor) SetErrorHandler(handler func(error)) {
	bp.mu.Lock()
	bp.errorHandler = handler
	bp.mu.Unlock()
}