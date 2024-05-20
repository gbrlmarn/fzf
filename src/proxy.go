package fzf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/junegunn/fzf/src/tui"
	"github.com/junegunn/fzf/src/util"
)

const becomeSuffix = ".become"

func escapeSingleQuote(str string) string {
	return "'" + strings.ReplaceAll(str, "'", "'\\''") + "'"
}

func fifo(name string) (string, error) {
	ns := time.Now().UnixNano()
	output := filepath.Join(os.TempDir(), fmt.Sprintf("fzf-%s-%d", name, ns))
	output, err := mkfifo(output, 0600)
	if err != nil {
		return output, err
	}
	return output, nil
}

func runProxy(commandPrefix string, cmdBuilder func(temp string) *exec.Cmd, opts *Options, withExports bool) (int, error) {
	output, err := fifo("proxy-output")
	if err != nil {
		return ExitError, err
	}
	defer os.Remove(output)

	// Take the output
	go func() {
		withOutputPipe(output, func(outputFile io.ReadCloser) {
			if opts.Output == nil {
				io.Copy(os.Stdout, outputFile)
			} else {
				reader := bufio.NewReader(outputFile)
				sep := opts.PrintSep[0]
				for {
					item, err := reader.ReadString(sep)
					if err != nil {
						break
					}
					opts.Output <- item
				}
			}
		})
	}()

	var command string
	commandPrefix += ` --proxy-script "$0"`
	if opts.Input == nil && util.IsTty() {
		command = fmt.Sprintf(`%s > %q`, commandPrefix, output)
	} else {
		input, err := fifo("proxy-input")
		if err != nil {
			return ExitError, err
		}
		defer os.Remove(input)

		go func() {
			withInputPipe(input, func(inputFile io.WriteCloser) {
				if opts.Input == nil {
					io.Copy(inputFile, os.Stdin)
				} else {
					for item := range opts.Input {
						fmt.Fprint(inputFile, item+opts.PrintSep)
					}
				}
			})
		}()

		if withExports {
			command = fmt.Sprintf(`%s < %q > %q`, commandPrefix, input, output)
		} else {
			// For mintty: cannot directly read named pipe from Go code
			command = fmt.Sprintf(`command cat %q | %s > %q`, input, commandPrefix, output)
		}
	}

	// To ensure that the options are processed by a POSIX-compliant shell,
	// we need to write the command to a temporary file and execute it with sh.
	var exports []string
	if withExports {
		exports = os.Environ()
		for idx, pairStr := range exports {
			pair := strings.SplitN(pairStr, "=", 2)
			exports[idx] = fmt.Sprintf("export %s=%s", pair[0], escapeSingleQuote(pair[1]))
		}
	}
	temp := WriteTemporaryFile(append(exports, command), "\n")
	defer os.Remove(temp)

	cmd := cmdBuilder(temp)
	if err := cmd.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			code := exitError.ExitCode()
			if code == ExitBecome {
				becomeFile := temp + becomeSuffix
				data, err := os.ReadFile(becomeFile)
				os.Remove(becomeFile)
				if err != nil {
					return ExitError, err
				}
				elems := strings.Split(string(data), "\x00")
				if len(elems) < 1 {
					return ExitError, errors.New("invalid become command")
				}
				command := elems[0]
				env := []string{}
				if len(elems) > 1 {
					env = elems[1:]
				}
				executor := util.NewExecutor(opts.WithShell)
				executor.Become(tui.TtyIn(), env, command)
			}
			return code, err
		}
	}

	return ExitOk, nil
}
