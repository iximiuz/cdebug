package cmd

import (
	"io"

	"github.com/docker/cli/cli/streams"
)

type Streams interface {
	InputStream() *streams.In
	OutputStream() *streams.Out
	ErrorStream() io.Writer
}

type CLI interface {
	Streams
}

type cli struct {
	inputStream  *streams.In
	outputStream *streams.Out
	errorStream  io.Writer
}

var _ CLI = &cli{}

func NewCLI(cin io.ReadCloser, cout io.Writer, cerr io.Writer) CLI {
	return &cli{
		inputStream:  streams.NewIn(cin),
		outputStream: streams.NewOut(cout),
		errorStream:  cerr,
	}
}

func (c *cli) InputStream() *streams.In {
	return c.inputStream
}

func (c *cli) OutputStream() *streams.Out {
	return c.outputStream
}

func (c *cli) ErrorStream() io.Writer {
	return c.errorStream
}
