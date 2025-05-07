package chromekiosk

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
)

type Monitor struct {
	StartUrl     string
	ProxyHandler http.Handler
	ImagePath    string
	MountPoint   string
	RunDir       string

	Browser Browser
	Con     Container

	proxyListener net.Listener

	browserErrc chan error
}

const (
	CageBin = "/usr/bin/cage"

	envPath         = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	xdgRuntimeDir   = "/run"
	userHome        = "/run/home"
	chromeDataDir   = "/run/chrome-data"
	proxyListenAddr = "127.0.0.1:8443"
	proxyListenUrl  = "https://" + proxyListenAddr
)

func (m *Monitor) Init() error {
	if path, err := filepath.Abs(m.RunDir); err != nil {
		return fmt.Errorf("RunDir abs %s: %w", m.RunDir, err)
	} else {
		m.RunDir = path
	}

	if m.MountPoint == "" {
		m.MountPoint = filepath.Join(m.RunDir, "mnt")
	}

	if m.StartUrl == "" {
		m.StartUrl = "blank:black"
	}

	*m = Monitor{
		ProxyHandler: m.ProxyHandler,
		ImagePath:    m.ImagePath,
		MountPoint:   m.MountPoint,
		RunDir:       m.RunDir,

		Browser: Browser{
			StartUrl: m.StartUrl,

			UserDataDir: chromeDataDir,

			ExecArgsPrefix: []string{CageBin, "--"},

			ExtraFlags: BrowserFlags{
				"proxy-server": proxyListenUrl,
			},

			CmdEnviron: []string{
				"PATH=" + envPath,
				"HOME=" + userHome,
				"XDG_RUNTIME_DIR=" + xdgRuntimeDir,
				"WLR_LIBINPUT_NO_DEVICES=1",
			},

			ConsoleLog: log.Default(),
		},

		Con: Container{
			Hostname:  "chromekiosk",
			Mount:     m.MountPoint,
			ImagePath: m.ImagePath,
			NsDir:     filepath.Join(m.RunDir, "ns"),
		},

		browserErrc: make(chan error, 1),
	}

	if err := m.Con.Init(); err != nil {
		return err
	}

	if err := m.Browser.Init(); err != nil {
		return err
	}

	return nil
}

func (m *Monitor) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := m.setup(); err != nil {
		return err
	}

	defer m.Con.Destroy()

	go func() {
		// FIXME
		err := runTLSProxy(ctx, m.proxyListener, m.ProxyHandler)
		if err != nil {
			log.Printf("runTLSProxy: %s", err)
		}
	}()

	go m.runBrowser(ctx)

	var (
		donec = ctx.Done()
	)

	for {
		select {
		case <-donec:
			return nil

		case err := <-m.browserErrc:
			log.Printf("browser error: %s", err)
			m.browserErrc = nil
			return err
		}
	}

	return nil
}

func (m *Monitor) setup() error {
	if err := mkdirAll(m.RunDir, 0o775); err != nil {
		return err
	}

	if err := m.Con.Create(); err != nil {
		return err
	}

	err := m.Con.Do(func() error {
		ln, err := net.Listen("tcp", proxyListenAddr)
		if err != nil {
			return err
		}

		m.proxyListener = ln
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func (m *Monitor) runBrowser(ctx context.Context) (err error) {
	defer func() {
		m.browserErrc <- err
		close(m.browserErrc)
	}()

	if err := m.Con.NsEnter(); err != nil {
		return err
	}

	return m.Browser.Run(ctx)
}
