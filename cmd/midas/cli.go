package main

import (
	"errors"
	"fmt"
	"github.com/CarlvinceTan/midas/internal/tui"
	"io"
	"strconv"
	"strings"
)

type cliOptions struct {
	cwd        string
	provider   string
	model      string
	agent      string
	session    string
	api        string
	baseURL    string
	maxTokens  int
	print      bool
	listModels bool
	listAgents bool
	help       bool
	version    bool
	prompt     string
}

func parseCLI(args []string, cwd string) (cliOptions, error) {
	cli := cliOptions{cwd: cwd}
	positionals := make([]string, 0)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		next := func() string {
			if index+1 >= len(args) {
				return ""
			}
			index++
			return args[index]
		}
		switch arg {
		case "--cwd", "-C":
			if value := next(); value != "" {
				cli.cwd = value
			}
		case "--model", "-m":
			cli.model = next()
		case "--provider":
			cli.provider = next()
		case "--agent", "-a":
			cli.agent = next()
		case "--session", "-s":
			cli.session = next()
		case "--api":
			cli.api = next()
		case "--base-url":
			cli.baseURL = next()
		case "--max-tokens":
			value := next()
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				return cli, errors.New("--max-tokens requires a non-negative integer")
			}
			cli.maxTokens = parsed
		case "--prompt":
			cli.prompt = next()
		case "--print", "-p":
			cli.print = true
		case "--list-models":
			cli.listModels = true
		case "--list-agents":
			cli.listAgents = true
		case "--help", "-h":
			cli.help = true
		case "--version":
			cli.version = true
		default:
			positionals = append(positionals, arg)
		}
	}
	if cli.prompt == "" {
		cli.prompt = strings.Join(positionals, " ")
	}
	return cli, nil
}

func writeUsage(output io.Writer) {
	fmt.Fprint(output, `midas - native Go coding agent

Usage:
  midas [options] [prompt...]

Options:
  -C, --cwd <dir>        Working directory (default: cwd)
  -m, --model <p/m>      Model as provider/model, or model ID with --provider
      --provider <name>   Provider: openai, anthropic, google, or compatible ID
  -a, --agent <name>     Agent profile
  -s, --session <id>     Resume an existing Midas session
  -p, --print            Stream one reply to stdout and exit
      --list-models      List available models and exit
      --list-agents      List Midas agent profiles and exit
  -h, --help             Show this help

Keys:
  enter      send / queue      cmd+enter steer / dequeue
  shift+enter newline          ctrl+c   abort / quit
  ctrl+t     cycle thinking
  esc        abort

Commands:
  /model /connect /agents /thinking /settings /voice /sessions /new /title /copy /undo /compact /stats /remote /goal /mcps /skills /exit
`)
}

func splitModel(provider, model string) (string, string) {
	return tui.SplitModelReference(provider, model)
}
