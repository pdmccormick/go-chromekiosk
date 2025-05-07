package chromekiosk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"os/exec"
	"strings"
	"syscall"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

var DefaultBrowserFlags = BrowserFlags{
	"kiosk":                          true,
	"no-default-browser-check":       true,
	"remote-debugging-port":          "9222",
	"no-sandbox":                     true,
	"disable-infobars":               true,
	"noerrdialogs":                   true,
	"enable-automation":              false,
	"disable-crash-report":           true,
	"ignore-certificate-errors":      true,
	"bwsi":                           true,
	"disable-extensions":             true,
	"allow-insecure-localhost":       true,
	"allow-running-insecure-content": true,

	// "unsafely-treat-insecure-origin-as-secure": "http://62mh.net",
	// "proxy-server":                             "http://127.0.0.1:9999",

	// "window-size":                    "1920,1080",
	// "window-position":                "0,0",

	"headless": false,

	"enable-features":        "UseOzonePlatform",
	"ozone-platform":         "wayland",
	"disable-blink-features": "AutomationControlled",
}

type BrowserFlags map[string]any

func (flags BrowserFlags) Clone() BrowserFlags { return maps.Clone(flags) }

func (flags BrowserFlags) toChromedp() (out []chromedp.ExecAllocatorOption) {
	out = make([]chromedp.ExecAllocatorOption, 0, len(flags))
	for k, v := range flags {
		out = append(out, chromedp.Flag(k, v))
	}
	return
}

const (
	DefaultChromeBin = "/opt/google/chrome/google-chrome"
	DefaultStartUrl  = "blank:black"

	DataUrlBgcolorFmt = `<style>html{background-color:%s}</style>`
)

type Browser struct {
	ChromeBin       string
	StartUrl        string
	Flags           BrowserFlags
	ExtraFlags      BrowserFlags
	CmdEnviron      []string
	CmdEnvironExtra []string
	CmdOutput       io.Writer
	ExecArgsPrefix  []string
	UserDataDir     string
	TraceLog        *log.Logger
	ConsoleLog      *log.Logger

	RunCtx     context.Context
	RunArgs    []string
	RunEnviron []string

	navigateOpc chan *browserNavigateOp
	evalOpc     chan *browserEvalOp
}

type browserNavigateOp struct {
	urlStr string
	errc   chan error
}

type browserEvalOp struct {
	code   string
	result []byte
	errc   chan error
}

func (br *Browser) Init() error {
	if br.ChromeBin == "" {
		br.ChromeBin = DefaultChromeBin
	}

	if br.StartUrl == "" {
		br.StartUrl = DefaultStartUrl
	}

	if len(br.Flags) == 0 {
		br.Flags = DefaultBrowserFlags.Clone()
	}

	maps.Copy(br.Flags, br.ExtraFlags)

	br.navigateOpc = make(chan *browserNavigateOp, 1)
	br.evalOpc = make(chan *browserEvalOp, 1)

	return nil
}

func (br *Browser) Run(ctx context.Context) error {
	ctx, cancel := br.setup(ctx)
	defer cancel()

	if dir := br.UserDataDir; dir != "" {
		if err := mkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if err := chromedp.Run(ctx); err != nil {
		return err
	}

	var (
		donec = ctx.Done()
	)

	for {
		select {
		case <-donec:
			return nil

		case op := <-br.navigateOpc:
			br.handleNavigate(ctx, op)

		case op := <-br.evalOpc:
			br.handleEval(ctx, op)
		}
	}

	return nil
}

func (br *Browser) setup(ctx context.Context) (context.Context, func()) {
	opts := br.setupOpts()

	ctx, cancel0 := chromedp.NewExecAllocator(ctx, opts...)

	ctx, cancel1 := chromedp.NewContext(ctx)

	cancelAll := func() {
		cancel1()
		cancel0()
	}

	chromedp.ListenTarget(ctx, br.listenTarget)

	br.RunCtx = ctx

	return ctx, cancelAll
}

func (br *Browser) setupOpts() (opts []chromedp.ExecAllocatorOption) {
	opts = append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(br.ChromeBin),
		chromedp.ModifyCmdFunc(br.modifyCmdFunc),
	)

	if w := br.CmdOutput; w != nil {
		opts = append(opts, chromedp.CombinedOutput(w))

	}

	if dir := br.UserDataDir; dir != "" {
		opts = append(opts, chromedp.UserDataDir(dir))
	}

	flags := br.Flags.toChromedp()
	opts = append(opts, flags...)

	return
}

func (br *Browser) modifyCmdFunc(cmd *exec.Cmd) {
	args := cmd.Args

	if prefix := br.ExecArgsPrefix; len(prefix) > 0 {
		args = append(prefix, args...)
		cmd.Path = args[0]
	}

	args[len(args)-1] = br.effectiveUrl(br.StartUrl)

	cmd.Args = args

	cmd.Dir = "/"
	cmd.Cancel = func() error { return br.cmdCancel(cmd) }
	if env := br.CmdEnviron; len(env) > 0 || env != nil {
		cmd.Env = br.CmdEnviron
	}

	if env := br.CmdEnvironExtra; len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}

	// If the parent monitor process (aka Goroutine) dies, kill the browser with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL,
	}

	br.RunArgs = args
	br.RunEnviron = cmd.Environ()
}

func (br *Browser) cmdCancel(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}

func (br *Browser) effectiveUrl(urlStr string) string {
	if urlStr == "" {
		urlStr = DefaultStartUrl
	}

	if colour, ok := strings.CutPrefix(urlStr, "blank:"); ok {
		html := fmt.Sprintf(DataUrlBgcolorFmt, colour)
		urlStr = br.dataUrlHtml(html)
	}

	return urlStr
}

func (br *Browser) dataUrlHtml(html string) string {
	return "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html))
}

func (br *Browser) listenTarget(ev any) {
	if logger := br.ConsoleLog; logger != nil {
		if ev, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			var args []any
			for _, arg := range ev.Args {
				if v := arg.Value; v != nil {
					args = append(args, v)
				}
			}

			if len(args) > 0 {
				logger.Printf("console %s: %+v", ev.Type, args)
			}
		}
	}

	if logger := br.TraceLog; logger != nil {
		out, err := json.Marshal(ev)
		if err == nil {
			logger.Printf("Event %T: %+v", ev, string(out))
		}
	}
}

func (br *Browser) Navigate(urlStr string) error {
	var op = browserNavigateOp{
		urlStr: urlStr,
		errc:   make(chan error, 1),
	}

	br.navigateOpc <- &op
	return <-op.errc
}

func (br *Browser) handleNavigate(ctx context.Context, op *browserNavigateOp) {
	var (
		urlStr = op.urlStr
		errc   = op.errc
	)

	urlStr = br.effectiveUrl(urlStr)
	errc <- chromedp.Run(ctx, chromedp.Navigate(urlStr))
}

func (br *Browser) EvalJS(code string) ([]byte, error) {
	var op = browserEvalOp{
		code: code,
		errc: make(chan error, 1),
	}

	br.evalOpc <- &op
	if err := <-op.errc; err != nil {
		return nil, err
	}
	return op.result, nil
}

func (br *Browser) JSEvalUnmarshal(code string, v any) error {
	var op = browserEvalOp{
		code: code,
		errc: make(chan error, 1),
	}

	br.evalOpc <- &op
	if err := <-op.errc; err != nil {
		return err
	}

	return json.Unmarshal(op.result, v)
}

func (br *Browser) handleEval(ctx context.Context, op *browserEvalOp) {
	var (
		code   = op.code
		result = op.result
		errc   = op.errc
	)

	errc <- chromedp.Run(ctx, chromedp.Evaluate(code, &result))
}
