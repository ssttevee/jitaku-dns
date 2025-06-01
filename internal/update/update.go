package update

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
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

func rangeReader(data []byte, start int64, length int64) (io.Reader, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to open gzip: %w", err)
	}

	if n, err := io.CopyN(io.Discard, gr, start); err != nil {
		return nil, fmt.Errorf("failed to skip %d bytes: %w", n, err)
	}

	return io.LimitReader(gr, length), nil
}

type partitionRange struct {
	start int64
	size  int64
}

func (r partitionRange) streamTo(target *updater.Target, name string, data []byte) error {
	rd, err := rangeReader(data, r.start, r.size)
	if err != nil {
		return fmt.Errorf("failed to create range reader: %w", err)
	}

	if err := target.StreamTo(context.TODO(), name, rd); err != nil {
		return err
	}

	return nil
}

type partitionRanges struct {
	boot partitionRange
	root partitionRange
}

func readPartitions(data []byte) (*partitionRanges, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to open gzip: %w", err)
	}

	// keep a 32KB buffer
	// the table should only be within the first 17KiB, but better safe than sorry
	table, err := partition.Read(&fileStub{r: bufio.NewReaderSize(gr, 32768)}, 512, 512)
	if err != nil {
		return nil, fmt.Errorf("failed to read partition table: %w", err)
	}

	parts := table.GetPartitions()
	if len(parts) != 4 {
		return nil, fmt.Errorf("expected 4 partitions, got %d", len(parts))
	}

	return &partitionRanges{
		boot: partitionRange{
			start: parts[0].GetStart(),
			size:  parts[0].GetSize(),
		},
		root: partitionRange{
			start: parts[1].GetStart(),
			size:  parts[1].GetSize(),
		},
	}, nil
}

func UpdateFromGZippedImage(data []byte) (reboot func() error, err error) {
	ranges, err := readPartitions(data)
	if err != nil {
		return nil, fmt.Errorf("failed to read partition ranges: %w", err)
	}

	url, err := gokrazyutil.DashboardURL()
	if err != nil {
		return nil, fmt.Errorf("failed to get dashboard url: %w", err)
	}

	target, err := updater.NewTarget(context.TODO(), url, http.DefaultClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create updater target: %w", err)
	}

	if err := ranges.root.streamTo(target, "root", data); err != nil {
		return nil, fmt.Errorf("failed to stream root partition: %w", err)
	}

	if err := ranges.boot.streamTo(target, "boot", data); err != nil {
		return nil, fmt.Errorf("failed to stream boot partition: %w", err)
	}

	// TODO: handle mbr systems

	return func() error {
		// switch to non-active partition
		if err := target.Switch(context.TODO()); err != nil {
			return fmt.Errorf("failed to switch root partition: %v", err)
		}

		if err := target.Reboot(context.TODO()); err != nil {
			return fmt.Errorf("failed to reboot: %v", err)
		}

		return nil
	}, nil
}
