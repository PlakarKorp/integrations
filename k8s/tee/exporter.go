package tee

import (
	"context"
	"errors"
	"fmt"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
)

// Exporter wraps an [exporter.Exporter] into a structure that's
// easier to "push" to.  If the exporter fails at any point, [Push]
// will return its failure.  In any case, call [Wait] when done to
// close the [exporter.Exporter].
type Exporter struct {
	ctx        context.Context
	err        <-chan error
	terminated bool
	exp        exporter.Exporter
	inner      struct {
		records chan *connectors.Record
		results chan *connectors.Result
	}
}

func New(ctx context.Context, exp exporter.Exporter) *Exporter {
	var (
		errch   = make(chan error, 1)
		records = make(chan *connectors.Record)
		results = make(chan *connectors.Result)
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
			records chan *connectors.Record
			results chan *connectors.Result
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

// Push sends a [connectors.Record] to the exporter and returns the
// result.  It can only fail if the exporter itself failed.
func (e *Exporter) Push(record *connectors.Record) (*connectors.Result, error) {
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
