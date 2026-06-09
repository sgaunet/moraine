package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// moveFile moves src to dst. It first tries os.Rename (atomic on the same
// volume); on a cross-device error it falls back to copy + fsync + remove
// (R9). dst must not already exist (callers use uniqueName).
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	if !isCrossDevice(err) {
		return err
	}
	return copyThenRemove(src, dst)
}

func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// copyThenRemove copies src to dst durably (fsync) then removes src. It uses
// O_EXCL so it can never overwrite an existing destination file.
func copyThenRemove(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("ouverture source: %w", err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("création destination: %w", err)
	}

	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst) // clean up partial copy
		return fmt.Errorf("copie: %w", err)
	}
	if err = out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("fsync: %w", err)
	}
	if err = out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("fermeture destination: %w", err)
	}
	if err = os.Remove(src); err != nil {
		return fmt.Errorf("suppression source après copie: %w", err)
	}
	return nil
}
