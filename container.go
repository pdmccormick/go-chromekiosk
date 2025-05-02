package chromekiosk

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func mkdirAll(name string, perm os.FileMode) error {
	if err := os.MkdirAll(name, perm); err != nil {
		return fmt.Errorf("MkdirAll %s: %w", name, err)
	}
	return nil
}

const (
	nsUts = iota
	nsNet
	nsMnt
	numNstypes
)

var nsTypes = []struct {
	name   string
	nstype int
}{
	{"uts", unix.CLONE_NEWUTS},
	{"net", unix.CLONE_NEWNET},
	{"mnt", unix.CLONE_NEWNS},
}

type Container struct {
	Hostname    string
	Mount       string
	ImagePath   string
	ImageFstype string
	NsDir       string

	nsProcBase string
	nsFds      [numNstypes]int
	nsPaths    [numNstypes]string
}

func (c *Container) Init() error {
	if path, err := filepath.Abs(c.Mount); err != nil {
		return fmt.Errorf("Abs Mount %s: %w", c.Mount, err)
	} else {
		c.Mount = path
	}

	if path, err := filepath.Abs(c.NsDir); err != nil {
		return fmt.Errorf("Abs NsDir %s: %w", c.NsDir, err)
	} else {
		c.NsDir = path
	}

	return nil
}

func (c *Container) Create() error {
	var (
		errc  = make(chan error, 1)
		donec = make(chan struct{}, 1)
	)

	defer close(donec)

	go func() {
		runtime.LockOSThread()
		errc <- c.createNs()
		<-donec
	}()

	if err := <-errc; err != nil {
		return err
	}

	return c.createFinalize()
}

func (c *Container) createFinalize() error {
	unix.Unmount(c.NsDir, unix.MNT_DETACH)

	if err := mkdirAll(c.NsDir, 0o755); err != nil {
		return err
	}

	for {
		if err := unix.Mount("", c.NsDir, "none", unix.MS_PRIVATE|unix.MS_REC, ""); err == nil {
			break
		}

		if err := mountBind(c.NsDir, c.NsDir); err != nil {
			return err
		}
	}

	for i, ns := range nsTypes {
		var (
			src = c.nsProcBase + ns.name
			dst = filepath.Join(c.NsDir, ns.name)
		)

		unix.Unmount(dst, unix.MNT_DETACH)
		os.Remove(dst)

		fd, err := unix.Creat(dst, 0o444)
		if err != nil {
			return fmt.Errorf("touch %s: %w", dst, err)
		}
		unix.Close(fd)

		if err := mountBind(src, dst); err != nil {
			return err
		}

		c.nsPaths[i] = dst
	}

	for i, path := range c.nsPaths {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}

		c.nsFds[i] = fd
	}

	return nil
}

func (c *Container) Destroy() error {
	for i, path := range c.nsPaths {
		unix.Unmount(path, unix.MNT_DETACH)
		os.Remove(path)
		c.nsPaths[i] = ""
	}

	unix.Unmount(c.NsDir, unix.MNT_DETACH)

	for i, fd := range c.nsFds {
		unix.Close(fd)
		c.nsFds[i] = -1
	}

	return nil
}

func (c *Container) NsEnter() error {
	runtime.LockOSThread()

	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("Unshare on Enter: %w", err)
	}

	var errs []error

	for i, ns := range nsTypes {
		fd := c.nsFds[i]
		if err := unix.Setns(fd, ns.nstype); err != nil {
			errs = append(errs, fmt.Errorf("Setns %d %s: %w", fd, ns.name, err))
		}
	}

	return errors.Join(errs[:]...)
}

func (c *Container) Do(runFunc func() error) error {
	var errc = make(chan error, 0)
	go func() {
		err := c.NsEnter()
		if err == nil {
			err = runFunc()
		}
		errc <- err
	}()
	return <-errc
}

func (c *Container) createNs() error {
	if err := unix.Unshare(
		unix.CLONE_NEWUTS |
			unix.CLONE_NEWNET |
			unix.CLONE_NEWNS,
	); err != nil {
		return fmt.Errorf("Unshare: %w", err)
	}

	c.nsProcBase = fmt.Sprintf("/proc/%d/task/%d/ns/", unix.Getpid(), unix.Gettid())

	if err := c.setupUts(); err != nil {
		return err
	}

	if err := c.setupNet(); err != nil {
		return err
	}

	if err := c.setupMount(); err != nil {
		return err
	}

	return nil
}

func (c *Container) setupUts() error {
	if err := unix.Sethostname([]byte(c.Hostname)); err != nil {
		return fmt.Errorf("Sethostname: %w", err)
	}

	return nil
}

func (c *Container) setupNet() error {
	linkLo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("LinkByName lo: %w", err)
	}

	if err := netlink.LinkSetUp(linkLo); err != nil {
		return fmt.Errorf("LinkSetUp lo: %w", err)
	}

	return nil
}

func (c *Container) setupMount() error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("mount rprivate /: %w", err)
	}

	var (
		newRoot  = c.Mount
		pivotOld = "/mnt"
		putOld   = filepath.Join(newRoot, pivotOld)
	)

	if image := c.ImagePath; image != "" {
		fstype := c.ImageFstype
		if fstype == "" {
			fstype = "squashfs"
		}

		if err := mkdirAll(newRoot, 0o755); err != nil {
			return err
		}

		if err := unix.Mount(image, newRoot, fstype, unix.MS_RDONLY, ""); err != nil {
			return fmt.Errorf("mount %s %s => %s: %w", fstype, image, newRoot, err)
		}
	} else {
		if err := mountBind(newRoot, newRoot); err != nil {
			return err
		}
	}

	if err := unix.PivotRoot(newRoot, putOld); err != nil {
		return fmt.Errorf("pivot_root %s %s: %w", newRoot, putOld, err)
	}

	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("Chdir: %w", err)
	}

	if err := unix.Unmount(pivotOld, unix.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount pivotold %s: %w", pivotOld, err)
	}

	if err := c.setupRootfs(); err != nil {
		return err
	}

	return nil
}

func (c *Container) setupRootfs() error {
	const (
		DefaultMountFlags = unix.MS_NOSUID | unix.MS_RELATIME | unix.MS_NODEV | unix.MS_NOEXEC
		DevMountFlags     = unix.MS_NOSUID | unix.MS_RELATIME | 0 | unix.MS_NOEXEC
		TmpMountFlags     = unix.MS_NOSUID | unix.MS_RELATIME | unix.MS_NODEV | 0
	)

	if err := unix.Mount("proc", "/proc", "proc", DefaultMountFlags, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}

	if err := unix.Mount("dev", "/dev", "devtmpfs", DevMountFlags, ""); err != nil {
		return fmt.Errorf("mount /dev: %w", err)
	}

	if err := unix.Mount("sys", "/sys", "sysfs", DefaultMountFlags, ""); err != nil {
		return fmt.Errorf("mount /sys: %w", err)
	}

	if err := unix.Mount("tmp", "/tmp", "tmpfs", TmpMountFlags, ""); err != nil {
		return fmt.Errorf("mount tmp: %w", err)
	}

	if err := unix.Mount("run", "/run", "tmpfs", DefaultMountFlags, ""); err != nil {
		return fmt.Errorf("mount tmp: %w", err)
	}

	return nil
}

func mountBind(src, dst string) error {
	if err := unix.Mount(src, dst, "none", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("mountBind %s => %s: %w", src, dst, err)
	}
	return nil
}
