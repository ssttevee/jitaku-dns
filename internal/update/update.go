package update

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"

	"github.com/diskfs/go-diskfs/partition"
	"github.com/gokrazy/updater"
	"github.com/ssttevee/jitaku-dns/internal/gokrazyutil"
)

type fileStub struct {
	r *bufio.Reader
}

// Close implements backend.File.
func (f *fileStub) Close() error {
	panic("unimplemented")
}

// Read implements backend.File.
func (f *fileStub) Read([]byte) (int, error) {
	panic("unimplemented")
}

// ReadAt implements backend.File.
func (f *fileStub) ReadAt(p []byte, off int64) (n int, err error) {
	log.Println("ReadAt", off, len(p))
	b, err := f.r.Peek(int(off) + len(p))
	if err != nil {
		return 0, err
	}

	copy(p, b[off:])
	return len(p), nil
}

// Seek implements backend.File.
func (f *fileStub) Seek(offset int64, whence int) (int64, error) {
	panic("unimplemented")
}

// Stat implements backend.File.
func (f *fileStub) Stat() (fs.FileInfo, error) {
	panic("unimplemented")
}

func UpdateFromGZippedImage(r io.Reader) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("failed to open gzip: %w", err)
	}

	// keep a 32KB buffer
	// the table should only be within the first 17KiB, but better safe than sorry
	br := bufio.NewReaderSize(gr, 32768)

	table, err := partition.Read(&fileStub{r: br}, 512, 512)
	if err != nil {
		return fmt.Errorf("failed to read partition table: %w", err)
	}

	parts := table.GetPartitions()
	if len(parts) != 4 {
		return fmt.Errorf("expected 4 partitions, got %d", len(parts))
	}

	url, err := gokrazyutil.DashboardURL()
	if err != nil {
		return fmt.Errorf("failed to get dashboard url: %w", err)
	}

	target, err := updater.NewTarget(url, http.DefaultClient)
	if err != nil {
		return fmt.Errorf("failed to create updater target: %w", err)
	}

	// the first two partitions are boot and rootfs, respectively.

	// skip to the start of the boot partition
	start0, err := br.Discard(int(parts[0].GetStart()))
	if err != nil {
		return fmt.Errorf("failed to skip to start of boot partition: %w", err)
	}

	// the root partition must be streamed first, so the boot partition must be buffered
	//
	// NOTE: this is around 100MB of memory, so it's not ideal
	bootbuf, err := io.ReadAll(io.LimitReader(gr, parts[0].GetSize()))
	if err != nil {
		return fmt.Errorf("failed to read boot partition: %w", err)
	}

	// skip to the start of the root partition
	if _, err := br.Discard(int(parts[1].GetStart()) - (int(start0) + len(bootbuf))); err != nil {
		return fmt.Errorf("failed to skip to start of root partition: %w", err)
	}

	if err := target.StreamTo("root", io.LimitReader(gr, parts[1].GetSize())); err != nil {
		return fmt.Errorf("failed to stream root partition: %w", err)
	}

	if err := target.StreamTo("boot", bytes.NewBuffer(bootbuf)); err != nil {
		return fmt.Errorf("failed to stream boot partition: %w", err)
	}

	// TODO: handle mbr systems

	// switch to non-active partition
	if err := target.Switch(); err != nil {
		return fmt.Errorf("failed to switching to non-active partition: %v", err)
	}

	if err := target.Reboot(); err != nil {
		return fmt.Errorf("failed to reboot: %v", err)
	}

	return nil
}
