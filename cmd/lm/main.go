package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type lineMsg struct{ line string }
type eofMsg struct{}
type tickMsg time.Time

func doTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func readLine(r *bufio.Reader) tea.Cmd {
	return func() tea.Msg {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && line != "" {
				return lineMsg{line: strings.TrimRight(line, "\n")}
			}
			return eofMsg{}
		}
		return lineMsg{line: strings.TrimRight(line, "\n")}
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	title := flag.String("title", "", "label shown on the left of the status bar (default: auto-detected source command)")
	flag.Parse()

	reader := bufio.NewReaderSize(os.Stdin, 64*1024)
	m := newModel(reader, *title)
	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	return err
}
