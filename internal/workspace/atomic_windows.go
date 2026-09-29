//go:build windows

package workspace

import (
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func atomicReplace(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".relay-save-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	// Antivirus scanners can briefly hold either path. Retry sharing violations
	// with a bounded delay; MoveFileEx performs the replacement as one operation.
	for i := 0; i < 5; i++ {
		err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 40 * time.Millisecond)
	}
	return err
}
