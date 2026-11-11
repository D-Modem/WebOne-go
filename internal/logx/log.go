// Package logx is WebOne's event log: lines go to the console (unless the
// proxy runs as a daemon) and to the log file, each request's lines
// stamped with the time the request began and the time since.
//
// Ported from WebOne's Log.cs.
package logx

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

const stampLayout = "02.01.2006 15:04:05.000"

var (
	mu      sync.Mutex
	file    io.WriteCloser
	console io.Writer = os.Stdout
	// Daemon hides console output (all goes to the log file only).
	Daemon bool
)

// SetConsole sends console output to w (tests).
func SetConsole(w io.Writer) {
	mu.Lock()
	console = w
	mu.Unlock()
}

// OpenLogFile starts writing the log to path (appending when append is set).
// An empty path stops file logging.
func OpenLogFile(path string, appendTo bool, version string, args []string) {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
		file = nil
	}
	if path == "" {
		fmt.Fprintln(console, "Not using log file.")
		return
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendTo {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		fmt.Fprintf(console, "Cannot use log file %s: %v.\n", path, err)
		return
	}
	file = f
	fmt.Fprintf(f, "%s\tWebOne %s%s (%s %s, %s) log started.\n",
		time.Now().Format("02.01.2006 15:04:05"), version, " "+strings.Join(args, " "),
		runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Fprintf(console, "Using event log file %s.\n", path)
}

// Enabled: a log file is open.
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return file != nil
}

// Close closes the log file.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
		file = nil
	}
}

func emit(msg string, display, write bool, displayText string) {
	mu.Lock()
	defer mu.Unlock()
	if display && !Daemon {
		if displayText == "" {
			fmt.Fprintln(console, msg)
		} else {
			fmt.Fprintln(console, displayText)
		}
	}
	if write && file != nil {
		fmt.Fprintln(file, msg)
	}
}

// Writer logs the lines of one request (or of the server itself).
type Writer struct {
	Begin time.Time
	first bool
}

// New is a Writer whose time starts now.
func New() *Writer { return &Writer{Begin: time.Now(), first: true} }

// Std is the server's own log writer.
var Std = New()

func (w *Writer) since() string {
	ticks := time.Since(w.Begin).Nanoseconds() / 100
	return fmt.Sprintf("%s+%d", w.Begin.Format(stampLayout), ticks/2)
}

// Printf logs a request line, displayed and timestamped.
func (w *Writer) Printf(format string, args ...any) { w.Log(true, true, format, args...) }

// Log writes a line. display: also to the console. stamped: a request line
// (">GET http://...") rather than a server message ("Starting ...").
func (w *Writer) Log(display, stamped bool, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	begin := w.Begin.Format(stampLayout)
	if !stamped {
		emit(begin+"+0\t"+msg, display, true, msg)
		return
	}
	if w.first {
		w.first = false
		emit(begin+"+0\t\t"+msg, display, false, "")
		emit(begin+"+0\t"+msg, false, true, "")
		return
	}
	emit(w.since()+"\t"+msg, display, true, "")
}

// Server logs a server message (not a request line).
func Server(display bool, format string, args ...any) { Std.Log(display, false, format, args...) }
