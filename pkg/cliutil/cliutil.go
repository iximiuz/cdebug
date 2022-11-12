package cliutil

import (
	"fmt"
	"io"
	"strings"

	"github.com/docker/cli/cli/streams"
)

type Streams interface {
	InputStream() *streams.In
	OutputStream() *streams.Out
	ErrorStream() io.Writer
}

type CLI interface {
	Streams

	SetQuiet(bool)

	// Regular print to stdout.
	Say(string, ...any)

	// Regular print to stderr.
	Grumble(string, ...any)

	// Optional print to stderr (unless quiet).
	Wisper(string, ...any)
}

type cli struct {
	inputStream  *streams.In
	outputStream *streams.Out
	errorStream  io.Writer

	quiet bool
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

func (c *cli) SetQuiet(v bool) {
	c.quiet = v
}

func (c *cli) Say(format string, a ...any) {
	fmt.Fprintf(c.OutputStream(), format, a...)
}

func (c *cli) Grumble(format string, a ...any) {
	fmt.Fprintf(c.ErrorStream(), format, a...)
}

func (c *cli) Wisper(format string, a ...any) {
	if !c.quiet {
		fmt.Fprintf(c.ErrorStream(), format, a...)
	}
}

type StatusError struct {
	status string
	code   int
}

var _ error = StatusError{}

func NewStatusError(code int, format string, a ...any) StatusError {
	status := strings.TrimSuffix(fmt.Sprintf(format, a...), ".") + "."
	return StatusError{
		code:   code,
		status: strings.ToUpper(status[:1]) + status[1:],
	}
}

func WrapStatusError(err error) error {
	if err == nil {
		return nil
	}
	return NewStatusError(1, err.Error())
}

func (e StatusError) Error() string {
	return e.status
}

func (e StatusError) Code() int {
	return e.code
}
