package tee

import (
	"context"
	"errors"
	"fmt"

	"github.com/PlakarKorp/integration-sdk"
)

// Exporter wraps an [integration.Exporter] into a structure that's
// easier to "push" to.  If the exporter fails at any point, [Push]
// will return its failure.  In any case, call [Wait] when done to
// close the [integration.Exporter].
type Exporter struct {
	ctx        context.Context
	err        <-chan error
	terminated bool
	exp        integration.Exporter
	inner      struct {
		records chan *integration.Record
		results chan *integration.Result
	}
}

func New(ctx context.Context, exp integration.Exporter) *Exporter {
	var (
		errch   = make(chan error, 1)
		records = make(chan *integration.Record)
		results = make(chan *integration.Result)
	)

	go func() {
		errch <- exp.Export(ctx, records, results)
		close(errch)
	}()

	return &Exporter{
		ctx: ctx,
		exp: exp,
		err: errch,
		inner: struct {
			records chan *integration.Record
			results chan *integration.Result
		}{
			records: records,
			results: results,
		},
	}
}

func (e *Exporter) failed(err error) error {
	e.terminated = true
	if err != nil {
		return fmt.Errorf("exporter failed: %w", err)
	}
	return fmt.Errorf("exporter exited successfully too early")
}

// Push sends a [integration.Record] to the exporter and returns the
// result.  It can only fail if the exporter itself failed.
func (e *Exporter) Push(record *integration.Record) (*integration.Result, error) {
	if e.terminated {
		return nil, fmt.Errorf("exporter already terminated")
	}

	// attempt to send the record, but catch a concurrent failure.
	select {
	case err := <-e.err:
		return nil, e.failed(err)

	case e.inner.records <- record:
		// sending does not mean we can receive, check if the
		// exporter failed without emitting a result.
		select {
		case res, ok := <-e.inner.results:
			if !ok {
				return nil, e.failed(nil)
			}
			return res, nil
		case err := <-e.err:
			return nil, e.failed(err)
		}
	}
}

// Wait signals and then waits for the exporter to terminate, it also
// takes care of closing it.
func (e *Exporter) Wait() (err error) {
	close(e.inner.records)
	if !e.terminated {
		err = errors.Join(err, <-e.err)
	}

	return errors.Join(err, e.exp.Close(e.ctx))
}
